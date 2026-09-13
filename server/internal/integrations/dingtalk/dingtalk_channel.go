// dingtalk_channel.go：把一条钉钉 Stream 连接适配成 engine.Supervisor 认识的
// channel.Channel——每份安装一个机器人的生命周期与入站消息的接入点。
//
// 角色：engine.Supervisor 通过注册的 Factory 为每个「活跃安装」构建一个
// dingtalkChannel（经由 RegisterDingTalk），并负责租约/重连生命周期。每个安装
// 各有自己的机器人（config 里各自的 AppKey + 加密的 AppSecret），所以每条连接
// 独立，和 Slack/飞书按安装拆适配器一致。
//
// 数据流：入站事件由 inbound.go 翻译，翻译时盖上【本安装】的 AppKey（钉钉回调
// 本身不带 robotCode），引擎 Router 靠它反查安装。出站回复走 EventChatDone 订
// 阅器（outbound.go）与 OutboundReplier；Send 满足 Channel 契约，用于群聊回复。
//
// 断线不丢任务：入站任务交给跨重连存活的 dispatcher（dispatch.go）串行处理，已
// ack 的消息在重连期间继续跑完，保证同一会话不乱序、也不并发。
package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// dingtalkChannel 是【一份安装】的钉钉 Stream 连接。每份安装各有自己的机器人——
// config 里自己的 AppKey 加加密 AppSecret——所以各有独立连接，与 Slack/飞书按
// 安装拆适配器一致。engine.Supervisor 通过注册的 Factory 给每个活跃安装建一个
// dingtalkChannel，并持有租约/重连生命周期；Connect 阻塞到 run context 被取消。
//
// 入站事件由 inbound.go 翻译、盖上【本安装】的 AppKey 以便引擎 Router 反查安装
// （钉钉回调不带 robotCode）。出站回复走 EventChatDone 订阅器（outbound.go）与
// OutboundReplier；Send 满足群聊回复的 Channel 契约。
type dingtalkChannel struct {
	appID     string // AppKey——盖进每条入站信封的路由键
	robotCode string
	appKey    string
	appSecret string // 解密后的明文——用来开 Stream 连接 + 铸令牌
	client    *Client
	botNames  *BotNameResolver
	handler   channel.InboundHandler
	logger    *slog.Logger
	// dispatch 把入站任务从 socket 读循环挪到每会话的串行队列上跑（见
	// dispatch.go）。在 factory 里每通道建一次；它跨重连存活，进行中的任务刻意
	// 比 socket 活得久。
	dispatch *dispatcher
	slot     *dispatchSlot
	// releaseDispatchSlot 在生命周期停止后，把已关闭的 slot 从 factory 注册表里
	// 摘掉。按 slot 比对，因此交叠的重建能用新的一代替换注册项、而不被旧一代误删。
	releaseDispatchSlot func()
	// stopDispatch 仅当传输返回时 run context 已被取消（吊销、丢租约、轮换或进程
	// 关停）才由 Connect 置位。正常的网关重连保持 false，让队列存活、保住跨重连的
	// 会话顺序。
	stopDispatch atomic.Bool
}

// inboundJob 是异步分发器收下的适配器私有任务负载。把原始回调一路带到 worker
// 真正跑起来才解析，让「机器人身份解析」可以发生在归一化之前，既不阻塞 Stream
// 读循环、也不把钉钉特有字段漏进 channel.InboundMessage。
type inboundJob struct {
	callback botCallbackData
}

// Type 返回渠道类型判别值（"dingtalk"），供引擎注册表匹配。
func (c *dingtalkChannel) Type() channel.Type { return TypeDingTalk }

// Capabilities 声明该渠道支持的能力：文本与附件（图片）。引擎据此决定媒体解析是
// 否进入管道。
func (c *dingtalkChannel) Capabilities() channel.Capability {
	return channel.CapText | channel.CapAttachment
}

// Disconnect 只在该次 Connect 观察到生命周期被取消时才排空分发队列。传输错误与
// 网关要求的重连【故意】保活队列，留给下一代 channel 继续用。slot 锁防止旧一代
// （已被取消的）去关闭已被替代者接管的队列。
func (c *dingtalkChannel) Disconnect(ctx context.Context) error {
	if !c.stopDispatch.Load() || c.slot == nil || c.dispatch == nil {
		return nil
	}
	c.slot.mu.Lock()
	if c.slot.current.Load() != c {
		c.slot.mu.Unlock()
		return nil
	}
	// 排空进行中的任务期间，current 仍指向这一代；关闭标记在 slot.mu 保护下先于
	// 任何「替代者查 slot」发布，所以 factory 会新建队列而不是接管它。
	c.dispatch.startClose()
	c.slot.mu.Unlock()
	if !c.dispatch.waitClosed(ctx) {
		go func() {
			<-c.dispatch.done
			c.releaseClosedDispatchSlot()
		}()
		return fmt.Errorf("dingtalk: dispatcher drain: %w", ctx.Err())
	}
	c.releaseClosedDispatchSlot()
	return nil
}

func (c *dingtalkChannel) releaseClosedDispatchSlot() {
	c.slot.current.CompareAndSwap(c, nil)
	if c.releaseDispatchSlot != nil {
		c.releaseDispatchSlot()
	}
}

// Send 用本安装的机器人把一条群聊回复发进 out.ChatID。它只满足 Channel 契约；主
// 回复路径（EventChatDone 订阅器与 OutboundReplier）会自己拿完整目标构造 sender。
func (c *dingtalkChannel) Send(ctx context.Context, out channel.OutboundMessage) (channel.SendResult, error) {
	s := &sender{client: c.client, robotCode: c.robotCode, appKey: c.appKey, appSecret: c.appSecret}
	key, err := s.send(ctx, sendTarget{ConversationType: convTypeGroup, ConversationID: out.ChatID}, out.Text)
	if err != nil {
		return channel.SendResult{}, err
	}
	return channel.SendResult{MessageID: key}, nil
}

// Connect 打开本安装的 Stream 连接（用自己的 AppKey/AppSecret 鉴权）并阻塞到 ctx
// 被取消。连接器只持有一条 socket 会话，ctx 取消、网关断连或 socket 断开即返回；
// 重连/退避与租约归 engine.Supervisor，所以这里报错只会触发一次受监督的重拨。
func (c *dingtalkChannel) Connect(ctx context.Context) (err error) {
	defer func() {
		// Supervisor 在每次 Connect 返回后都会取消自己的子 context，所以这个判决
		// 必须在返回 Supervisor 之前就地捕获。
		c.stopDispatch.Store(ctx.Err() != nil)
	}()
	if c.handler == nil {
		return errors.New("dingtalk: inbound handler not configured")
	}
	if c.appSecret == "" {
		return errors.New("dingtalk: app secret not configured")
	}
	conn := &wsConnector{
		httpClient: c.client.httpClient,
		apiBase:    c.client.apiBase,
		appKey:     c.appKey,
		appSecret:  c.appSecret,
		onMessage:  c.onMessage,
		logger:     c.logger,
	}
	return conn.run(ctx)
}

// onMessage 是连接器的机器人消息回调。它用【本安装】的 AppKey 翻译事件并塞进每
// 会话的分发队列，socket 读循环因此立刻 ack、绝不被管道里的慢活（媒体下载可能花
// 几十秒）卡住。它永远返回 nil：钉钉不会重投机器人消息，引擎的 (installation,
// msgId) 去重反正会兜住任何重复。
func (c *dingtalkChannel) onMessage(ctx context.Context, data *botCallbackData) error {
	if data == nil || data.SenderStaffId == "" {
		// 这条消息根本不会进引擎（没有发送者 staff id——系统事件或机器人自己发的），
		// 也就没有对应的 channel_inbound_audit 行。把丢弃记成日志而不是无声消失，好
		// 让报表可诊断。（畸形/超额的媒体现在已经能作为「图片不可用」占位符进引擎。）
		if data != nil {
			c.logger.InfoContext(ctx, "dingtalk: dropped unsupported inbound message",
				"app_id", c.appID, "msg_type", data.Msgtype, "msg_id", data.MsgId,
				"has_sender", data.SenderStaffId != "")
		}
		return nil
	}
	job := inboundJob{callback: cloneBotCallbackData(data)}
	c.dispatch.enqueue(data.ConversationId, job)
	return nil
}

// cloneBotCallbackData 拷贝一条回调的浅层字段，并把切片字段（AtUsers、Content）
// 复制成独立底层数组——入队后原始回调仍可能被调用方改动，任务队列里必须是一份自
// 己持有内存的完整快照。
func cloneBotCallbackData(data *botCallbackData) botCallbackData {
	cloned := *data
	cloned.AtUsers = append([]botCallbackAtUser(nil), data.AtUsers...)
	cloned.Content = append(json.RawMessage(nil), data.Content...)
	return cloned
}

// runInbound 是分发器每一条任务的执行体。先把当前机器人的可读名字解析出来，再
// 扁平化回调，把归一化后的消息交给共享引擎；管道错误按旧内联路径同样的方式上报。
func (c *dingtalkChannel) runInbound(ctx context.Context, job inboundJob) {
	data := &job.callback
	botName := ""
	if data.ConversationType == convTypeGroup && data.IsInAtList && c.botNames != nil {
		resolved, err := c.botNames.resolveCredentials(ctx, credentials{
			AppKey: c.appKey, AppSecret: c.appSecret, RobotCode: c.robotCode,
		}, data.ConversationId)
		if err != nil {
			if errors.Is(err, errMissingChatManagePermission) {
				c.logger.DebugContext(ctx, "dingtalk: Bot name unavailable without qyapi_chat_manage",
					"app_id", c.appID, "conversation_id", data.ConversationId)
			} else {
				c.logger.WarnContext(ctx, "dingtalk: could not resolve Bot name before normalization",
					"error", err, "app_id", c.appID, "conversation_id", data.ConversationId)
			}
		} else {
			botName = resolved
		}
	}
	msg, ok := inboundFromCallbackWithBotName(data, c.appID, botName)
	if !ok {
		c.logger.InfoContext(ctx, "dingtalk: dropped unsupported inbound message",
			"app_id", c.appID, "msg_type", data.Msgtype, "msg_id", data.MsgId,
			"has_sender", data.SenderStaffId != "")
		return
	}
	if err := c.handler(ctx, msg); err != nil {
		c.logger.WarnContext(ctx, "dingtalk: inbound handler error", "error", err, "app_id", c.appID)
		c.notifyIssueDispatchError(msg)
	}
}

// issueErrorReplyTimeout 限制脱钩的错误回复发送时长。
const issueErrorReplyTimeout = 5 * time.Second

const issueDispatchFailedText = "⚠️ I couldn't create that issue because an internal error occurred. Please try again."

// notifyIssueDispatchError 在「@ 到机器人的 /issue 命令」于引擎管道内失败时（共享
// issue 命令路径报出 Result 之前的一次性 resolver/DB 错误），补发一条内部错误提示。
// 该帧早已 ack 且不会重投，没有它命令就会无声消失。脱钩成 goroutine，让 onMessage
// 能立即返回。
func (c *dingtalkChannel) notifyIssueDispatchError(msg channel.InboundMessage) {
	if !isAddressedIssueCommand(msg) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), issueErrorReplyTimeout)
		defer cancel()
		s := &sender{client: c.client, robotCode: c.robotCode, appKey: c.appKey, appSecret: c.appSecret}
		if _, err := s.send(ctx, targetFromMessage(msg), issueDispatchFailedText); err != nil {
			c.logger.WarnContext(ctx, "dingtalk: issue dispatch-error reply failed",
				"error", err, "app_id", c.appID)
		}
	}()
}

// ChannelDeps 是钉钉 Factory 闭包捕获的共享依赖。引擎入站 handler 每次构建时经
// channel.Config.Handler 传入；Decrypter 把库里加密的 AppSecret 还原成明文；
// Client 持有出站令牌缓存与传输层；BotNames 与「群存在性发现」共享，让摄取归一化
// 与元数据观察共用同一份权威身份缓存。
type ChannelDeps struct {
	Decrypt  Decrypter
	Client   *Client
	BotNames *BotNameResolver
	Logger   *slog.Logger
}

// dispatchSlot 让同一个「会话分发器」跨 supervisor 为 Stream 重连而重建的多个
// channel 对象存活。每次重连前替换 current，保证队列里的任务用的是最新的凭据与
// client。
type dispatchSlot struct {
	mu      sync.Mutex
	current atomic.Pointer[dingtalkChannel]
	queue   *dispatcher
}

// dispatchSlotRegistry 按 AppKey 维护每安装一份的 dispatchSlot，是重连代际之间
// 复用/替换队列的注册表。
type dispatchSlotRegistry struct {
	mu      sync.Mutex
	byAppID map[string]*dispatchSlot
}

func newDispatchSlotRegistry() *dispatchSlotRegistry {
	return &dispatchSlotRegistry{byAppID: make(map[string]*dispatchSlot)}
}

// assign 把刚构建的 channel 挂到它所属的 slot 上：首次见到该 AppKey 就新建 slot
// 与队列；已有关闭的队列则建新的；仍在运行的队列直接复用以保会话顺序。
func (r *dispatchSlotRegistry) assign(ch *dingtalkChannel, logger *slog.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()

	slot := r.byAppID[ch.appID]
	if slot != nil {
		slot.mu.Lock()
	}
	if slot == nil || slot.queue.isClosed() {
		if slot != nil {
			slot.mu.Unlock()
		}
		slot = &dispatchSlot{}
		slot.mu.Lock()
		slot.queue = newDispatcher(func(ctx context.Context, job inboundJob) {
			if current := slot.current.Load(); current != nil {
				current.runInbound(ctx, job)
			}
		}, logger)
		r.byAppID[ch.appID] = slot
	}
	slot.current.Store(ch)
	ch.dispatch = slot.queue
	ch.slot = slot
	ch.releaseDispatchSlot = func() { r.release(ch.appID, slot) }
	slot.mu.Unlock()
}

// release 尝试把 slot 从注册表摘掉；只有仍指向它（没被重建替换）且已无人使用
// （current 已清空、队列已关闭）时才真正删除。
func (r *dispatchSlotRegistry) release(appID string, slot *dispatchSlot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byAppID[appID] != slot {
		return
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.current.Load() == nil && slot.queue.isClosed() {
		delete(r.byAppID, appID)
	}
}

// size 返回当前注册的 slot 数（测试用）。
func (r *dispatchSlotRegistry) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byAppID)
}

// RegisterDingTalk 注册按安装拆分的钉钉 Factory，让 engine.Supervisor 为每个活跃
// 安装构建并监督一个 dingtalkChannel。
func RegisterDingTalk(reg *channel.Registry, deps ChannelDeps) {
	reg.Register(TypeDingTalk, newDingTalkFactory(deps))
}

func newDingTalkFactory(deps ChannelDeps) channel.Factory {
	return newDingTalkFactoryWithRegistry(deps, newDispatchSlotRegistry())
}

// newDingTalkFactoryWithRegistry 用注入的 slot 注册表构造 Factory（测试可传入假
// 注册表断言复用/替换行为）。返回的构建函数在每次重连构建 channel 时，把通道挂到
// 注册表按 AppKey 分好的 slot 上。
func newDingTalkFactoryWithRegistry(deps ChannelDeps, slots *dispatchSlotRegistry) channel.Factory {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	dtClient := deps.Client
	if dtClient == nil {
		dtClient = NewClient(nil, "")
	}
	return func(cfg channel.Config) (channel.Channel, error) {
		var ic installConfig
		if err := json.Unmarshal(cfg.Raw, &ic); err != nil {
			return nil, fmt.Errorf("dingtalk: decode installation config: %w", err)
		}
		appSecret, err := decryptToken(ic.AppSecretEncrypted, deps.Decrypt)
		if err != nil {
			return nil, fmt.Errorf("dingtalk: decrypt app secret: %w", err)
		}
		if appSecret == "" {
			return nil, errors.New("dingtalk: installation has no app secret")
		}
		ch := &dingtalkChannel{
			appID:     ic.AppID,
			robotCode: ic.robotCodeOrAppID(),
			appKey:    ic.AppID,
			appSecret: appSecret,
			client:    dtClient,
			botNames:  deps.BotNames,
			handler:   cfg.Handler,
			logger:    logger,
		}

		// Supervisor.Build 每次重连都会跑一遍。按本安装唯一的 AppKey 复用队列，
		// 避免「重连前还在途的那轮」与「重连后收到的下一轮」并发跑。
		slots.assign(ch, logger)
		return ch, nil
	}
}
