// wecom_channel.go —— engine.Supervisor 驱动的 Channel + Factory，外加一条 aibot
// 智能机器人连接的 WebSocket 运行循环。企业微信每台机器人只允许一条活跃连接；
// Supervisor 的 WS 租约在进程层强制同样的「每副本至多一条」不变量，两者叠加，
// 就在不需要 wecom 特有协调的前提下给了我们「每条安装全局单连接」。
//
// 读取循环放在本文件（而不是像 lark/ws_connector.go 那样放进一个共享连接器），
// 是因为 aibot 协议小到「按安装循环」比「抽一层 EventConnector 抽象」更清晰。
// Slack 在 slack_channel.go 里也是同样的形态——按安装的接收循环直接内联在
// Channel.Connect 里。
package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	cryptorand "crypto/rand"
	"encoding/hex"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
)

// DefaultWSURL 是 aibot 长连接端点。企微对每个机器人发布同一个全局端点；WS 握手
// 后 aibot_subscribe 帧里携带的 (bot_id, secret) 对标识这条连接属于哪台机器人。
const DefaultWSURL = "wss://openws.work.weixin.qq.com"

// pingInterval 是客户端主动的心跳节奏。企微文档规定 30s；低于它对方可能杀掉
// socket，高于它就是我们刷屏。
const pingInterval = 30 * time.Second

// subscribeTimeout 限定「发出 aibot_subscribe」到「收到 errcode 0 的 ack」之间
// 的等待。服务端实际几百毫秒内就回；这个上限防的是静默的 socket。
const subscribeTimeout = 10 * time.Second

// readDeadline 在每次成功读后刷新。窗口内没有字节到达就假设 socket 已死、强制
// 关闭——随后交给 Supervisor 去重连。它必须比 pingInterval 大出舒适的余量，让
// pong 不会迟到到触发误判。
const readDeadline = 90 * time.Second

// writeDeadline 限定单帧的写预算。宁可要一条真慢的 socket，也不要一个无限卡住
// 的 goroutine。
const writeDeadline = 10 * time.Second

// handshakeTimeout 约束初始 TCP + WS 握手拨号。
const handshakeTimeout = 15 * time.Second

// unsupportedMsgTypeReceipt 是对「本适配器完全读不了的消息」回的那一行。它以前写
// 的是 "我目前只能处理文字消息"——纯文字——但从照片、文件、视频和图文混排开始
// 路由的那一刻起，这话就不对了：一个刚看着机器人回答完截图、转头被告知它只能处
// 理文字的人，读出来的意思是「机器人坏了」，而不是「这一种类型不支持」。
const unsupportedMsgTypeReceipt = "抱歉，我暂时无法处理这类消息。"

// wecomChannel 是一条安装的 aibot 智能机器人 WebSocket 连接。engine.Supervisor
// 通过注册的 Factory 为每条活跃安装建一条，并驱动租约 / 重连生命周期；Connect
// 阻塞在接收循环上，直到 ctx 被取消或链路断开。
type wecomChannel struct {
	// dedup claims the inbound message id for the one reply this adapter sends
	// before the Router sees the message: the unsupported-kind receipt. It goes
	// out from dispatchFrame, ahead of c.handler, so the Router's own Claim
	// never runs for it and nothing else can dedupe it. Nil leaves it
	// unclaimed, which is main's behaviour.
	dedup engine.Deduper

	installationID pgtype.UUID
	botID          string
	secret         string
	// botDisplayName 是这台机器人在会话里的名字，来自安装配置。尚未填它的安装为
	// 空；空名字退回什么见 stripLeadingMentions。
	botDisplayName string
	handler        channel.InboundHandler
	dialer         Dialer
	wsURL          string
	logger         *slog.Logger
	// senders 是包级的 installation→wsSender 注册表（见 senders_registry.go）。
	// 我们持有引用，好让 Connect 进入时登记、退出时清除。不锻炼 OutboundReplier
	// 路径的测试里为 nil。
	senders *sendersRegistry
	// metrics 是健康信号出口（metrics.go）。绝不直接读——一律走 mx()，它会为没
	// 带出口构建的 channel 换上空实现。
	metrics Metrics
}

var _ channel.Channel = (*wecomChannel)(nil)

func (c *wecomChannel) Type() channel.Type { return TypeWecom }

// mx 返回一个永远可以安全调用的出口。测试用字面量构造 wecomChannel、不给出
// 口；关掉 /metrics 的部署刻意接线 nil。
func (c *wecomChannel) mx() Metrics {
	if c.metrics == nil {
		return nopMetrics{}
	}
	return c.metrics
}

// Capabilities 声明 aibot 适配器目前支持什么。入站附件会被下载、解密并绑定
// （media_ingest.go），所以 CapAttachment 成立的方向和钉钉一致
// （dingtalk_channel.go:52）。把媒体发回去是另一回事——它需要企微的
// aibot_upload_media_* 握手——本文件不声称这项能力。
func (c *wecomChannel) Capabilities() channel.Capability {
	return channel.CapText | channel.CapAttachment
}

// Disconnect 是空操作：WS 连接的整个生命周期都局限在 Connect 内（run context
// 一取消它就返回），所以这里没有需要释放的长生命周期资源。与 feishuChannel /
// slackChannel 对齐。
func (c *wecomChannel) Disconnect(ctx context.Context) error { return nil }

// Connect 拨号 aibot 长连接端点、发订阅帧，然后跑读取循环直到 ctx 被取消或链路
// 断开。每个退出路径都会取消派生的 runCtx 并等待读 goroutine 观察到它，所以一
// 次瞬时失败会在 Supervisor 重连之前就把活连接拆干净——不会泄漏一个把事件灌进无
// 人读 channel 的 socket goroutine。
func (c *wecomChannel) Connect(ctx context.Context) (err error) {
	if c.handler == nil {
		return errors.New("wecom: inbound handler not configured")
	}
	if c.botID == "" || c.secret == "" {
		return errors.New("wecom: bot_id / secret not configured")
	}

	wsURL := c.wsURL
	if wsURL == "" {
		wsURL = DefaultWSURL
	}
	if _, err := url.Parse(wsURL); err != nil {
		return fmt.Errorf("wecom: parse ws url: %w", err)
	}

	dialer := c.dialer
	if dialer == nil {
		dialer = defaultDialer
	}

	log := c.logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("installation_id", util.UUIDToString(c.installationID), "bot_id", c.botID)

	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		c.mx().RecordConnectFailure()
		return fmt.Errorf("wecom: dial %s: %w", wsURL, err)
	}
	defer conn.Close()

	sender := newWSSender(conn, log)

	// 看门狗：把 ctx 取消桥接到阻塞的 ReadMessage() 调用上。gorilla 的
	// ReadMessage 不感知 ctx；取消我们的 ctx 只会翻转 ctx.Done，并不会碰到读系统
	// 调用。我们在 ctx.Done 时关掉 socket，让在途的 Read 立即以 "use of closed
	// connection" 错误返回。看门狗也会在（经 done 的）任何其他退出路径上跑，保证
	// 这个 goroutine 永不泄漏；close 是幂等的，正常退出时双关也是安全的。
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)

	// 订阅——给连接做鉴权。这里任何错误都会把循环交还给 Supervisor 去退避重试。
	if err := c.subscribe(ctx, conn, sender, log); err != nil {
		return err
	}
	log.Info("wecom: subscribe ok")

	// 把 sender 装进包级注册表，让 OutboundReplier（启动时建的，不是按安装建的）
	// 能按 installation id 找到这条连接、经同一条 socket 推 aibot_send_msg。退出
	// 时清除，绝不给一条已死连接的僵尸 sender 派活。
	if c.senders != nil && c.installationID.Valid {
		c.senders.set(c.installationID, sender)
		defer c.senders.clear(c.installationID, sender)
	}

	// 心跳——企微约 90s 后杀静默 socket。我们每 30s 经共享的写互斥锁 ping 一次，
	// 让它和其他出站帧干净交错。
	pingCtx, pingCancel := context.WithCancel(ctx)
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		c.pingLoop(pingCtx, sender, log)
	}()
	// 在一个 defer 里按这个顺序做「取消 + 等待」。拆成两个 defer 会按 LIFO 跑——
	// 先等、后取消——而 pingLoop 只在 pingCtx.Done 后才返回。任何父 ctx 仍然活着
	// 的退出路径（读错误、分发错误）上将永远无人取消 pingCtx，等待会永远卡住，
	// Connect 也永远不会把控制权还给 Supervisor 去重连。
	defer func() {
		pingCancel()
		<-pingDone
	}()

	// 入站回调跑在它们自己的 worker 上，不跑在读取循环上。读取循环是服务端裁定
	// 的唯一投递者，所以任何内联处理回调的东西都不能再去等它自己写的那帧的 ack
	// ——那等于等自己。钉钉适配器已经是这个形状。
	//
	// 只用一个 worker、不用池：企微按序投递一个会话的消息，而引擎的去重与轮次批
	// 处理都假设那个顺序能存活。
	//
	// 队列满了会 BLOCK 读取循环，而不是丢消息。背压的代价是一次重连；丢弃的代价
	// 是一条用户消息、且无从解释。这个成立的前提是仍有人在接收，所以读取循环的
	// send 同时盯着 cbDone——见下面的发送点。
	callbacks := make(chan frameEnvelope, callbackQueueDepth)
	cbDone := make(chan struct{})
	var cbErr error
	go func() {
		defer close(cbDone)
		for env := range callbacks {
			if e := c.dispatchFrame(ctx, env, sender, log); e != nil {
				cbErr = e
				// 若读取循环正卡在 ReadMessage 里，唤醒它；光取消 ctx 推不动它。
				// 卡在队列发送上的读取循环改由 cbDone 唤醒——关 socket 推不动一
				// 个 channel 发送。
				_ = conn.Close()
				return
			}
		}
	}()
	defer func() {
		close(callbacks)
		<-cbDone
		// worker 的错误才是真实原因；随之而来的读错误只是「为走到这里」关掉的
		// socket。只在 ctx 仍活着时采用：关停或租约丢失的取消若恰好逮住一条在飞
		// 的回调，那是普通停止，把那条回调的错误升上来会报出虚假的「连接带错退
		// 出」。
		if cbErr != nil && ctx.Err() == nil {
			err = cbErr
		}
	}()

	// 读取循环。每帧都走同一条 解码 → 分发 →（可能）回复 的路径。单帧坏帧不会
	// 拆掉 socket——只有传输 / handler 错误才会升级。
	for {
		if ctx.Err() != nil {
			return nil
		}
		// 在读取前立刻布防，别处都不布防。
		//
		// 它以前在 ReadMessage 返回之后才布防，把循环随后做的一切都放进窗口内。
		// 静默机器人上只有服务端的 pong 会重置 deadline、而我们的 ping 每 30s 出
		// 一次，所以负载重的池上，下一次读可能在一把完全健康的 socket 上超时。
		// 空闲窗口量度的应当就是空闲本身。
		//
		// 这个错误不再被丢弃：拒绝设 deadline 的 socket 不值得继续读。
		if err := conn.SetReadDeadline(time.Now().Add(readDeadline)); err != nil {
			// 关停路径会关闭 socket，而关闭的 socket 拒绝设 deadline。那是普通停
			// 止，不是要上报给 Supervisor 的失败。
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("wecom: set read deadline: %w", err)
		}
		typ, payload, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("wecom: read: %w", err)
		}
		if typ != websocket.TextMessage && typ != websocket.BinaryMessage {
			continue
		}

		var env frameEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			log.Warn("wecom: bad frame envelope", "error", err, "size", len(payload))
			continue
		}
		traceIn(log, env)
		switch env.Cmd {
		case cmdMsgCallback, cmdEventCallback:
			select {
			case callbacks <- env:
				c.mx().RecordCallbackQueued()
				continue
			default:
				// worker 落后了。阻塞是刻意的选择（见下面），也是运维要知道的事
				// ——从这一刻起 socket 不再被排空，若持续下去企微会顶掉这条连接。
				c.mx().RecordCallbackQueueBlocked()
			}
			select {
			case callbacks <- env:
				c.mx().RecordCallbackQueued()
			case <-cbDone:
				// worker 已停，这次发送没有接收者——也没有关闭者：队列要由一条
				// 必须等本循环返回才能跑的 defer 关闭。队列满时这就是永久停泊，
				// 因为 worker 的 conn.Close() 唤醒的是坐在 ReadMessage 上的读取
				// 循环，不是坐在发送上的那个，而这条路径上 ctx 一直活着。那将永
				// 远没有重连：Supervisor 会为一条已停止读取的连接不断续租。
				//
				// 返回，让 defer 里的 handler 用 worker 的错误（真正的根因）来顶
				// 替。
				return nil
			case <-ctx.Done():
				return nil
			}
		default:
			// ack、ping、pong 留在读取循环上：它们是 worker 自己那些写正在等的
			// 帧。
			if err := c.dispatchFrame(ctx, env, sender, log); err != nil {
				return err
			}
		}
	}
}

// callbackQueueDepth 是回调 worker 落后读取循环多远之后、读取循环才阻塞。超过
// 它 socket 不再被排空、企微发现后顶掉连接——这是正确结局：跟不上的副本本应把
// 机器人交给跟得上的那个，而不是悄悄丢弃它够不到的消息。
const callbackQueueDepth = 64

// subscribe 发出 aibot_subscribe 帧，并等待（至多 subscribeTimeout）服务端 ack。
// ack 形态是一帧回显 headers.req_id + errcode 的帧；errcode == 0 即成功。
//
// 非零 errcode 走 classifySubscribeAck——安装时凭证探测对同一个 ack 用的也是这
// 个函数，所以两者对同一个码不可能答出不同的答案。40001 / 40013 归成
// ErrCredentialsRejected：这种拒绝会在每次退避后原样重演，直到有人修好安装。其余
// 非零码一律 ErrCredentialsUnverifiable，因为限流（45009、45033）或平台侧故障会
// 自行消退，把它算成凭证失败会为一个机器人明明没问题的租户去 paging 运维。两个
// 哨兵都导出，让 channel/engine——正是从 Connect() 收到这个错误的那层——可以分
// 支处理。
func (c *wecomChannel) subscribe(ctx context.Context, conn wsConn, sender *wsSender, log *slog.Logger) error {
	reqID := newReqID()
	if err := sender.write(map[string]any{
		"cmd":     cmdSubscribe,
		"headers": frameHeaders{ReqID: reqID},
		"body":    subscribeBody(c.botID, c.secret),
	}); err != nil {
		c.mx().RecordConnectFailure()
		return fmt.Errorf("wecom: send subscribe: %w", err)
	}

	// 等待配对的 ack——服务端把它写成 cmd 为空（或缺失）、headers.req_id 与我们
	// 相同的一帧。先到的任何其他帧都被丢弃（订阅是最早的交换，实际很少发生）。
	deadline := time.Now().Add(subscribeTimeout)
	_ = conn.SetReadDeadline(deadline)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		typ, payload, err := conn.ReadMessage()
		if err != nil {
			// socket 死了、ack 在 subscribeTimeout 内没到、或我们自己的 ctx 在读
			// 中途被取消、看门狗在脚下关掉了 socket。基建问题或关停——两者都不必
			// 特意告诉谁，下一次退避很可能就成功了。滚动重启因此会在这里加几个
			// 计数；看的是速率，几个不足为道。
			c.mx().RecordConnectFailure()
			return fmt.Errorf("wecom: subscribe read: %w", err)
		}
		if typ != websocket.TextMessage && typ != websocket.BinaryMessage {
			continue
		}
		var env frameEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}
		// 在 req_id 过滤之前就追踪：一次被拒的订阅、或用我们从未发出的 req_id 回
		// 答的订阅，正是运维开 trace 要看的那种失败。
		traceIn(log, env)
		if env.Headers.ReqID != reqID {
			continue
		}
		if env.ErrCode != 0 {
			// 进两个计数器中的哪一个，取决于这个 ack 意味着什么——而
			// classifySubscribeAck 已经定了这个：和安装时凭证探测拿到的是同一个
			// 裁定。按它的回答分支，而不是在这里再测一遍 errcode：rejectionErrCodes
			// 再抄一份就随时可能和探测的那份漂移，然后仪表盘和安装页会对同一个码
			// 给出矛盾的说法。
			err := classifySubscribeAck(log, env.ErrCode, env.ErrMsg)
			if errors.Is(err, ErrCredentialsRejected) {
				// 凭其本身被拒：密钥错、机器人已删。单独计数、区别于其他所有连接
				// 失败，因为它是唯一「每次退避都原样重演、直到有人改点什么」的失
				// 败。
				c.mx().RecordAuthFailure()
			} else {
				// 无法核验——限流或平台侧故障。它会自行消退，和一次没落地的拨号
				// 一模一样，所以该进那个计数器、不该进运维读作「去轮换一个凭证」
				// 的那个。
				c.mx().RecordConnectFailure()
			}
			return err
		}
		return nil
	}
}

// dispatchFrame 路由一条服务端帧。只有 aibot_msg_callback 会（作为 handler 基建
// 失败）升级回循环的调用方；事件记日志 + 回 ack，其余一切静默丢弃。
func (c *wecomChannel) dispatchFrame(ctx context.Context, env frameEnvelope, sender *wsSender, log *slog.Logger) error {
	switch env.Cmd {
	case cmdMsgCallback:
		var mc aibotMsgCallback
		if err := json.Unmarshal(env.Body, &mc); err != nil {
			log.Warn("wecom: bad aibot_msg_callback body", "error", err)
			return nil
		}
		text, ok := mc.ownText()
		// 用解析后的正文追踪，而不是 mc.Text.Content：后者对每条语音、媒体和图
		// 文混排回调都是空的，追踪它会恰好给「运维开 trace 要看的那些消息」打出
		// len=0。
		traceInbound(log, mc, text)
		msg := channelMessageFromCallback(c.botID, c.botDisplayName, mc, text, env.Headers.ReqID)
		if !ok {
			// 这条消息里没有任何可读的东西：一种适配器不认识的类型（位置卡片），
			// 或一种认识、却缺了让它可用的那个字段到达——识别返回空的语音、不带
			// url 的图片回调。沉默读起来像机器人坏了，所以往同一个会话回一行回执
			// 就停。尽力而为：发送失败退化成原来的静默丢弃。
			log.Debug("wecom: unsupported message kind, replying with a receipt", "msg_type", mc.MsgType, "msg_id", mc.MsgID)
			c.sendUnsupportedReceipt(ctx, sender, mc.MsgID, msg.Source.ChatID,
				aibotChatTypeFromChannel(msg.Source.ChatType), unsupportedMsgTypeReceipt, log)
			return nil
		}
		if err := c.handler(ctx, msg); err != nil {
			return err
		}
		return nil
	case cmdEventCallback:
		var ec aibotEventCallback
		if err := json.Unmarshal(env.Body, &ec); err != nil {
			log.Warn("wecom: bad aibot_event_callback body", "error", err)
			return nil
		}
		switch ec.Event.EventType {
		case eventDisconnected:
			// 另一条连接顶掉了我们的。返回让 Supervisor 退避重连（它随后又会顶掉
			// 那一条——后写者胜）。
			return errors.New("wecom: received disconnected_event (superseded)")
		default:
			log.Debug("wecom: event", "type", ec.Event.EventType)
			return nil
		}
	case cmdServerPing:
		// 服务端发起的 ping（按文档很少见，但防御性处理）。
		if err := sender.write(map[string]any{
			"cmd":     cmdPong,
			"headers": frameHeaders{ReqID: env.Headers.ReqID},
		}); err != nil {
			return fmt.Errorf("wecom: pong: %w", err)
		}
		return nil
	case cmdPong:
		// 对我们客户端主动 ping 的 ack——空操作。
		return nil
	default:
		// 对我们某次写出的匿名 ack 帧（cmd 为空）。多数是 errcode=0 的空操作，
		// 但 aibot_send_msg / aibot_respond_msg / aibot_upload_media_* 可能带非零
		// errcode 拒绝（比如 msgtype 错、被限流、会话不可写）。把错误记进日志，
		// 让一次失败的出站无需抓包就能看见。
		// 若有谁在等，交给写那帧的人。无人认领的 ack 不是错误——不等待裁定的推送
		// 与请求共用这条连接。
		if sender.routeResponse(env) {
			return nil
		}
		if env.ErrCode != 0 {
			log.Warn("wecom: server ack error",
				"errcode", env.ErrCode,
				"errmsg", env.ErrMsg,
				"req_id", env.Headers.ReqID,
			)
		}
		return nil
	}
}

// pingLoop 每隔 pingInterval 发一次心跳帧，直到 ctx 被取消。写失败会在下一条
// ReadMessage 的错误路径上浮出水面；我们在这里记日志，但不自己拆循环。
func (c *wecomChannel) pingLoop(ctx context.Context, sender *wsSender, log *slog.Logger) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := sender.write(map[string]any{
				"cmd":     cmdPing,
				"headers": frameHeaders{ReqID: newReqID()},
			}); err != nil {
				log.Warn("wecom: ping write failed", "error", err)
			}
		}
	}
}

// Send 是引擎用规范化 OutboundMessage 调用的出站 Channel 入口。第一版迭代一律用
// aibot_send_msg（企微的「主动推送」cmd）而不是 aibot_respond_msg——send_msg 没
// 有 5 秒期限，且无论消息是否要回连到某条特定入站帧都能用。唯一的麻烦是
// chat_type：aibot_send_msg 需要知道 ChatID 是单聊用户 id 还是群 id。我们借用内
// 部客服号用过的长度启发式（chat id ≥33 字符、userid 更短），实践中它很稳定。
//
// Channel 不是 multica 引擎里主要的出站路径——EventChatDone 订阅者和
// OutboundReplier 处理大多数发送——但 Channel.Send 仍是让引擎投递临时回复的契约，
// 所以为与 feishuChannel / slackChannel 对齐，我们还是实现了它。
func (c *wecomChannel) Send(ctx context.Context, out channel.OutboundMessage) (channel.SendResult, error) {
	// 不用它。wecom 出站走 OutboundReplier / Outbound（EventChatDone +
	// EventInboxNew），它们知道消息真实的 Source.ChatType、发给正确的会话。通用
	// 的 Channel.Send 接缝对本通道从不会被引擎调用；这里过去的 stub 用
	// len(ChatID) > 32 猜单聊还是群聊——本包唯一的会话类型推断。与其保留第二条
	// 启发式出站路径，不如直接回 not-supported。
	_ = ctx
	_ = out
	return channel.SendResult{}, ErrSendNotSupported
}

// ErrSendNotSupported 由 wecomChannel.Send 返回。企微的通用 Channel.Send 接缝没
// 有诚实的实现——channel.OutboundMessage 不带 chat_type，而出站本来就走
// OutboundReplier / Outbound——它们从入站帧上读会话类型。
var ErrSendNotSupported = errors.New("wecom: Channel.Send is not supported; outbound goes through OutboundReplier/Outbound")

// ---- factory ----

// ChannelDeps 打包 wecom Factory 闭包要用的共享依赖。引擎入站 handler 经
// channel.Config.Handler 按构建提供；CredentialsResolver 解密已存的密钥。
type ChannelDeps struct {
	// Dedup claims the unsupported-kind receipt so a redelivered callback does
	// not answer twice. Build it with NewDeduper(store) — the same store the
	// Router's own claim uses, so a message answered here can never also be
	// answered there. Nil keeps main's behaviour: every redelivery repeats the
	// receipt, and RegisterWecom says so at WARN.
	Dedup engine.Deduper

	Credentials CredentialsResolver
	Logger      *slog.Logger

	// Senders 是包级的 installation→wsSender 注册表。OutboundReplier 和 Outbound
	// 都经它查活 wsSender。启动传一份与 OutboundReplier 构造器共享的同一实例。
	// 不锻炼出站的测试里为 nil。
	Senders *sendersRegistry

	// Metrics 是每条建出来的 channel 上报的健康出口。nil 丢弃所有计数器——关掉
	// /metrics 的部署正是这样。
	Metrics Metrics

	// Dialer 覆盖默认 gorilla 拨号器。测试把它指向 httptest server；生产留 nil。
	Dialer Dialer

	// WSURL 覆盖 DefaultWSURL。与 Dialer 一样的测试专用意图。
	WSURL string
}

// sendUnsupportedReceipt answers a message kind the adapter cannot read, once
// per message rather than once per delivery.
//
// WeCom redelivers a callback it did not get an ack for, and the receipt is
// sent from dispatchFrame — before c.handler, so the Router's own Claim
// (router.go) never runs for this message and nothing downstream deduplicates
// it. Unclaimed, every redelivery of one unreadable photo puts another "sorry,
// I can only read text" in the chat, and each copy spends one of the
// conversation's active pushes.
//
// The claim is the same two-phase token the Router uses, on the same table and
// the same (installation, message) key, so a message answered here can never
// also be answered there.
//
// Three outcomes, and the failure directions are chosen so the user's worst
// case is the behaviour they already had:
//
//   - already claimed → the receipt is on their screen; say nothing.
//   - sent → Mark it processed, and later redeliveries stop at the claim.
//   - send failed → Release, so the NEXT redelivery may try again. Holding the
//     claim would turn one failed send into permanent silence for a message
//     the user is waiting on an answer for.
//
// A dedup that errors (or is not wired) falls through and sends anyway: with
// the database unreachable the choice is a possible duplicate against a bot
// that looks broken, and silence is the worse of the two. That is the same
// stance the send itself takes.
func (c *wecomChannel) sendUnsupportedReceipt(
	ctx context.Context,
	sender *wsSender,
	messageID, chatID string,
	chatType int,
	content string,
	log *slog.Logger,
) {
	var (
		token   pgtype.UUID
		claimed bool
	)
	if c.dedup != nil && messageID != "" {
		tok, err := c.dedup.Claim(ctx, c.installationID, messageID)
		switch {
		case errors.Is(err, engine.ErrDuplicate):
			// A redelivery of a message already answered. The receipt the
			// user is meant to read is the one they already have.
			log.Debug("wecom: unsupported-kind receipt already sent for this message, not repeating",
				"msg_id", messageID)
			return
		case err != nil:
			log.Warn("wecom: cannot claim the unsupported-kind receipt, sending it unclaimed",
				"error", err, "msg_id", messageID)
		default:
			token, claimed = tok, true
		}
	}

	if err := sender.sendText(chatID, chatType, content); err != nil {
		log.Debug("wecom: unsupported-kind receipt send failed", "error", err, "msg_id", messageID)
		if claimed {
			// Nothing was delivered, so the claim must not stand: it would
			// suppress the redelivery that is this message's only remaining
			// chance of being answered at all.
			if rerr := c.dedup.Release(ctx, c.installationID, messageID, token); rerr != nil {
				log.Warn("wecom: cannot release the unsupported-kind receipt claim; a redelivery will "+
					"now be silently dropped", "error", rerr, "msg_id", messageID)
			}
		}
		return
	}
	if claimed {
		if err := c.dedup.Mark(ctx, c.installationID, messageID, token); err != nil {
			log.Warn("wecom: cannot mark the unsupported-kind receipt processed; a redelivery may repeat it",
				"error", err, "msg_id", messageID)
		}
	}
}

// RegisterWecom registers the per-installation wecom smart-bot Factory so
// the engine.Supervisor builds + supervises one wecomChannel per active
// installation. "Adding wecom smart-bot inbound" is this call plus the
// adapter — no engine edit (same contract as lark.RegisterFeishu /
// slack.RegisterSlack).
func RegisterWecom(reg *channel.Registry, deps ChannelDeps) {
	if deps.Dedup == nil {
		// Not fatal — answering twice is what main does today — but it has no
		// other symptom: nothing errors, and the duplicate only shows up in
		// somebody's chat, one per redelivery, each spending a push from that
		// conversation's quota.
		logger := deps.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("wecom: no deduper wired; a redelivered unreadable message will be answered again " +
			"on every delivery (set ChannelDeps.Dedup to NewDeduper(store))")
	}
	reg.Register(TypeWecom, newWecomFactory(deps))
}

func newWecomFactory(deps ChannelDeps) channel.Factory {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(cfg channel.Config) (channel.Channel, error) {
		if deps.Credentials == nil {
			return nil, errors.New("wecom: credentials resolver missing")
		}
		var ic installConfig
		if len(cfg.Raw) > 0 {
			if err := json.Unmarshal(cfg.Raw, &ic); err != nil {
				return nil, fmt.Errorf("wecom: decode installation config: %w", err)
			}
		}
		if ic.BotID == "" {
			return nil, errors.New("wecom: installation config missing bot_id")
		}
		inst := Installation{BotID: ic.BotID, SecretEncrypted: ic.SecretEncrypted}
		creds, err := deps.Credentials.Credentials(inst)
		if err != nil {
			return nil, fmt.Errorf("wecom: decrypt secret: %w", err)
		}
		return &wecomChannel{
			installationID: cfg.ID,
			dedup:          deps.Dedup,
			botID:          creds.BotID,
			secret:         creds.Secret,
			botDisplayName: ic.BotDisplayName,
			handler:        cfg.Handler,
			dialer:         deps.Dialer,
			wsURL:          deps.WSURL,
			logger:         logger,
			senders:        deps.Senders,
			metrics:        orNopMetrics(deps.Metrics),
		}, nil
	}
}

// ---- request id ----

// newReqID 为一条 WebSocket 帧的 headers.req_id 返回随机关联 id。服务端在每个
// ack 上把它回显，客户端靠它把回复与请求配对。
func newReqID() string {
	var buf [8]byte
	if _, err := cryptorand.Read(buf[:]); err != nil {
		return fmt.Sprintf("wecom-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
