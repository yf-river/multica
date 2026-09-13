// ws_connector.go：生产环境的 EventConnector——持有飞书长连接 WebSocket、
// 解码开放平台服务器推来的二进制 Frame 信封，把规范化事件交给上游分发。
package lark

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSLongConnConnector 是生产环境的 EventConnector：保持 Lark 长连接 WebSocket
// 打开，解码开放平台服务器推来的二进制 Frame 信封，并把规范化事件转发给
// dispatcher。
//
// 协议层（与 `larksuite/oapi-sdk-go/v3/ws` 对齐）：
//
//  1. EndpointFetcher 做 POST /callback/ws/endpoint 引导。Lark 返回一个带
//     `device_id` + `service_id` 查询参数作为凭据的一次性 wss URL。`service_id`
//     被提取出来，作为每条出站帧的 Frame.Service。
//  2. 每个 WebSocket 帧都是二进制 protobuf Frame。数据事件里 JSON 信封包在
//     Frame.Payload 内；control / ping / pong / ack 帧是纯二进制的。
//  3. client 按服务器在引导 ClientConfig 里返回的 PingInterval 发应用层 ping 帧
//     （NewPingFrame）。不使用 WebSocket 协议层 PING——Lark 服务器会无视它。
//     服务器也能随时推 ping；我们回 NewPongFrame。
//  4. 每个数据帧都要回 ACK。ACK 原样复用入站帧的 Headers（Lark 按 message_id
//     关联），并在 Payload 里写一个 JSON Response{code:200, ...}。Dispatcher
//     emit 成功就回 ACK 200；Dispatcher 上报基础架构失败时回 500，让 Lark 重试。
//
// §4.4 不变量的归属（ctx 取消要能打断阻塞读）：
//
// gorilla/websocket.ReadMessage 阻塞在 TCP socket 上，观察不到 context。取消
// ctx 会触发 ctx.Done，但碰不到 read 系统调用。我们用看门狗 goroutine 把
// ctx → 读中断桥接起来：ctx 一旦触发就调 conn.Close；gorilla 的 Close 会让任何
// 在途 Read 立刻带着 "use of closed connection" 错误返回。看门狗在正常退出时
// 也会跑（绝不泄漏 goroutine），且幂等——conn.Close 可安全多次调用。任何未来
// 协议改动都必须保住这条不变量：没了它，renewLeaseUntil 的 cancelRun 就毫无
// 意义，两个副本可能会在“连接健康但静默”期间处理同一条安装。
//
// PersonalAgent 兼容性风险：官方飞书文档把长连接描述成“仅支持企业自建应用”。
// PersonalAgent 设备流 app 不在支持列表里。若引导调用返回 Lark 的结构化错误，
// 本 connector 会带着包装后的错误退出 Run，Hub 的退避循环会在每次重试时记录它
// ——让错误配置显形。冒烟路径见 MUL-2671 评审线程。
type WSLongConnConnector struct {
	cfg WSConnectorConfig
}

// WSConnectorConfig 装配 connector 的依赖。所有注入接口都必须提供；nil 依赖会
// 让 NewWSLongConnConnector 返回错误，而不是产出一个首次使用就 panic 的
// connector。时间/日志字段在构造时兜底。
type WSConnectorConfig struct {
	// Dialer 打开 WebSocket 传输。默认 gorilla 的 DefaultDialer 带限定的
	// HandshakeTimeout。测试注入指向 httptest server 的 fake。
	Dialer WSDialer

	// EndpointFetcher 经引导 POST 解析每个安装的 WS URL + 服务器配置（ping
	// 间隔、service id）。connector 每次 Run 调一次，所以这里的瞬时失败触发
	// Hub 级退避重试，而不是 Run 内重连风暴。
	EndpointFetcher EndpointFetcher

	// FrameDecoder 把单个已解码 Frame 变成规范化 InboundMessage（向上游 emit）
	// 或“control / 心跳 / 未知”信号（connector 静默丢弃）。decoder 的错误不会
	// 退出循环——只记日志并丢弃——因为单条畸形 Lark 事件负载不该拆掉整条连接。
	FrameDecoder FrameDecoder

	// Enricher 可选地先用用户显式附上的上下文（引用回复 / 转发包）展开已解码
	// 消息的正文，再 emit 给 dispatcher。它跑在入站读循环上，受 EnrichTimeout
	// 约束以保护 Lark 长连接的 ACK 预算；超时/拉取失败时 enricher 退化成
	// 占位符而不是阻塞。nil 表示禁用富化（解码后的正文原样 emit）。
	Enricher Enricher

	// EnrichTimeout 限制单条消息的富化耗时（至多两次 GetMessage 调用）。它必须
	// 明显小于 Lark ~3 秒的长连接 ACK 窗口，因为富化跑在帧被 ACK 之前。零值
	// 默认 2 秒。
	EnrichTimeout time.Duration

	// CredentialsProvider 返回 EndpointFetcher 需要的 InstallationCredentials。
	// 通常包一层 InstallationService.DecryptAppSecret，让明文 secret 不驻留在
	// 内存里的 LarkInstallation 行上。
	CredentialsProvider CredentialsProvider

	// PingInterval 是应用层 ping 的兜底节奏。生产里会被引导 ClientConfig 里
	// Lark 返回的 PingInterval 按安装覆盖。零值默认 2 分钟（与
	// `larksuite/oapi-sdk-go/v3/ws/client.go` 的 SDK 默认一致）。
	PingInterval time.Duration

	// ReadDeadline 限制单次 ReadMessage 调用。每次读前重新武装；到期产出一个
	// 瞬时读错误，connector 记日志后借此退出，把重连退避交给 Hub。零值默认 6
	// 分钟，带 2 分钟默认 ping 的健康连接不会触到它。
	ReadDeadline time.Duration

	// WriteTimeout 限制单次 WriteMessage。零值默认 10 秒。
	WriteTimeout time.Duration

	// ChunkTTL 限制分块组装器在丢弃缓冲分块前保留半成品多帧事件多久。镜像 SDK
	// 5 秒默认——足够吸收多块间的节奏，又短到被放弃的多帧事件不会泄漏内存。
	// 零值默认 5 秒。
	ChunkTTL time.Duration

	// Now 可覆盖，用于确定性测试。默认 time.Now。
	Now func() time.Time

	// Logger 可选；默认 slog.Default。
	Logger *slog.Logger
}

// withDefaults 填充配置默认值。
func (c WSConnectorConfig) withDefaults() WSConnectorConfig {
	if c.PingInterval == 0 {
		c.PingInterval = 2 * time.Minute
	}
	if c.ReadDeadline == 0 {
		c.ReadDeadline = 6 * time.Minute
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 10 * time.Second
	}
	if c.ChunkTTL == 0 {
		c.ChunkTTL = 5 * time.Second
	}
	if c.EnrichTimeout == 0 {
		c.EnrichTimeout = 2 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// NewWSLongConnConnector 校验配置并返回一个可复用的 connector。
func NewWSLongConnConnector(cfg WSConnectorConfig) (*WSLongConnConnector, error) {
	if cfg.Dialer == nil {
		return nil, errors.New("lark ws connector: Dialer is required")
	}
	if cfg.EndpointFetcher == nil {
		return nil, errors.New("lark ws connector: EndpointFetcher is required")
	}
	if cfg.FrameDecoder == nil {
		return nil, errors.New("lark ws connector: FrameDecoder is required")
	}
	if cfg.CredentialsProvider == nil {
		return nil, errors.New("lark ws connector: CredentialsProvider is required")
	}
	return &WSLongConnConnector{cfg: cfg.withDefaults()}, nil
}

// Run 实现 EventConnector。开一条 WebSocket 会话、读二进制 Frame 信封，直到 ctx
// 被取消或连接出错，然后返回。返回 nil = 干净退出；返回非 nil = 连接失败
// （Hub 提高退避档位）。
func (c *WSLongConnConnector) Run(ctx context.Context, inst Installation, emit EventEmitter) error {
	log := c.cfg.Logger.With(
		"installation_id", uuidString(inst.ID),
		"app_id", inst.AppID,
	)

	creds, err := c.cfg.CredentialsProvider.Credentials(ctx, inst)
	if err != nil {
		return fmt.Errorf("resolve credentials: %w", err)
	}

	endpoint, err := c.cfg.EndpointFetcher.Endpoint(ctx, creds)
	if err != nil {
		return fmt.Errorf("resolve ws endpoint: %w", err)
	}

	// 服务器推的 PingInterval 优先于静态默认——这是 SDK 行为。零值（服务器省略
	// 该字段）退回静态默认，绝不会退化到“每 0 秒 ping 一次”。
	pingInterval := endpoint.PingInterval
	if pingInterval <= 0 {
		pingInterval = c.cfg.PingInterval
	}

	conn, _, err := c.cfg.Dialer.DialContext(ctx, endpoint.URL, endpoint.Headers)
	if err != nil {
		return fmt.Errorf("dial ws: %w", err)
	}

	// runCtx 在【每次】 Run 退出时都向看门狗与 ping goroutine 扇出取消，而不仅
	// 在外层 ctx 取消时。否则读错误或 emit 基础架构失败会把 ping goroutine 留在
	// 外层 ctx 上滴答——而 defer 的 join 会死锁。
	runCtx, runCancel := context.WithCancel(ctx)

	var closeOnce sync.Once
	closeConn := func() {
		closeOnce.Do(func() {
			_ = conn.Close()
		})
	}

	// 看门狗：ctx 触发 → 关 socket，让阻塞的 ReadMessage 立即返回。也跑在任何
	// 其它退出路径上，保证绝不泄漏 goroutine。
	done := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			closeConn()
		case <-done:
		}
	}()

	// writeMu 序列化来自读循环（ACK 发送）与 ping goroutine 的 WriteMessage。
	// gorilla/websocket 禁止并发写者；没有它，ack 与 ping 交错会毁掉二进制帧流。
	var writeMu sync.Mutex

	// 每次 Run 独立的分块组装器。状态不需要活得比单次长连接会话更久——飞书在
	// 重连后会从第 0 块重新下发多帧事件——所以组装器在这里构建、Run 返回时
	// 丢弃，同时释放被放弃事件持有的任何半成品缓冲。
	assembler := newChunkAssembler(c.cfg.ChunkTTL, c.cfg.Now)

	// Ping 循环：按服务器的 PingInterval 发应用层二进制 ping 帧。
	pingDone := make(chan struct{})
	go c.pingLoop(runCtx, conn, &writeMu, endpoint.ServiceID, pingInterval, log, pingDone)

	defer func() {
		runCancel()
		closeConn()
		close(done)
		<-pingDone
	}()

	log.Info("lark ws connector: connected",
		"service_id", endpoint.ServiceID,
		"ping_interval", pingInterval.String(),
	)

	for {
		// 每次读前重新武装读截止时间，让卡死的连接最终能解开系统调用。
		if err := conn.SetReadDeadline(c.cfg.Now().Add(c.cfg.ReadDeadline)); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("set read deadline: %w", err)
		}

		msgType, raw, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				log.Info("lark ws connector: ctx cancelled, read returned",
					"close_err", err.Error(),
				)
				return nil
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Info("lark ws connector: server closed connection", "err", err.Error())
				return nil
			}
			return fmt.Errorf("read message: %w", err)
		}
		// 飞书只发二进制帧。文本帧属于飞书侧 schema 回归——记日志并丢弃，图个
		// 安全。
		if msgType != websocket.BinaryMessage {
			log.Warn("lark ws connector: dropped non-binary frame",
				"type", msgType,
				"len", len(raw),
			)
			continue
		}

		frame, err := UnmarshalFrame(raw)
		if err != nil {
			log.Warn("lark ws connector: frame protobuf decode failed",
				"err", err.Error(),
				"raw_len", len(raw),
			)
			continue
		}

		// Control 帧承载 ping / pong / 配置更新。我们只需响应 ping（回 pong）；
		// pong 与配置负载静默接受。
		if frame.Method == FrameMethodControl {
			if frame.HeaderValue(FrameHeaderTypeKey) == FrameHeaderTypePing {
				if werr := c.writeFrame(&writeMu, conn, NewPongFrame(endpoint.ServiceID)); werr != nil {
					log.Warn("lark ws connector: pong write failed", "err", werr.Error())
				}
			}
			continue
		}

		// 多帧事件：先存分块、跳过 ACK，直到完整负载到齐。镜像 SDK 的
		// combine() 行为——SDK 不对半成品分块 ACK；飞书用自己的 sum/seq 在侧边
		// 协调投递，所以对半成品 ACK 等于在真正拿到拼装负载之前告诉 Lark “我们
		// 收到了”。
		sum, seq, msgID := parseChunkHeaders(frame)
		payload := frame.Payload
		if sum > 1 {
			assembled, complete := assembler.admit(msgID, sum, seq, frame.Payload)
			if !complete {
				log.Debug("lark ws connector: partial chunk buffered",
					"message_id", msgID,
					"seq", seq,
					"sum", sum,
					"pending", assembler.pendingCount(),
				)
				continue
			}
			payload = assembled
			log.Debug("lark ws connector: chunk reassembly complete",
				"message_id", msgID,
				"chunks", sum,
				"bytes", len(payload),
			)
		}

		// 数据帧：把（可能已拼装的）JSON 负载交给 decoder，解析成消息就 emit，
		// 并回 ACK。
		msg, ok, derr := c.cfg.FrameDecoder.Decode(payload, inst)
		if derr != nil {
			log.Warn("lark ws connector: frame decode failed",
				"err", derr.Error(),
				"payload_len", len(frame.Payload),
			)
			// 解码失败也回 200 ACK：消息在线上是合法的，只是我们无法处理它。
			// NACK 会触发飞书侧对一份已证明解析不了的负载的重试风暴。
			if werr := c.writeFrame(&writeMu, conn, NewAckFrame(frame, true)); werr != nil {
				log.Warn("lark ws connector: ack-after-decode-error write failed", "err", werr.Error())
				return fmt.Errorf("write ack: %w", werr)
			}
			continue
		}
		if !ok {
			// 心跳 / 未处理的事件类型。回 ACK 200 让服务器停止重发；“我们处理
			// 什么”的策略归 decoder 所有。
			if werr := c.writeFrame(&writeMu, conn, NewAckFrame(frame, true)); werr != nil {
				log.Warn("lark ws connector: ack-after-drop write failed", "err", werr.Error())
				return fmt.Errorf("write ack: %w", werr)
			}
			continue
		}

		// emit 之前先用显式附上的上下文（引用回复 / 转发包）富化解码后的正文。
		// 这跑在帧被 ACK 之前，受 EnrichTimeout 约束，失败退化成占位符而不是阻塞
		// 管线。大多数消息无需富化，不发任何网络调用就直接返回。
		if c.cfg.Enricher != nil {
			enrichCtx, cancelEnrich := context.WithTimeout(ctx, c.cfg.EnrichTimeout)
			msg = c.cfg.Enricher.Enrich(enrichCtx, msg, creds)
			cancelEnrich()
		}

		_, emitErr := emit(ctx, msg)
		if emitErr != nil {
			// Dispatcher 的基础架构失败（DB 挂等）。NACK 让 Lark 在健康副本上
			// 重试该事件；然后返回，让 Hub 退避并重连。
			if werr := c.writeFrame(&writeMu, conn, NewAckFrame(frame, false)); werr != nil {
				log.Warn("lark ws connector: nack write failed", "err", werr.Error())
			}
			log.Error("lark ws connector: emit infra error",
				"event_id", msg.EventID,
				"err", emitErr.Error(),
			)
			return fmt.Errorf("dispatch: %w", emitErr)
		}
		if werr := c.writeFrame(&writeMu, conn, NewAckFrame(frame, true)); werr != nil {
			log.Warn("lark ws connector: ack write failed", "err", werr.Error())
			return fmt.Errorf("write ack: %w", werr)
		}
	}
}

// writeFrame 序列化一个 Frame 并在连接写互斥锁下以二进制 WebSocket 消息写出。
// 调用方持锁时【不得】调用本方法。
func (c *WSLongConnConnector) writeFrame(mu *sync.Mutex, conn WSConn, f *Frame) error {
	payload := f.Marshal()
	mu.Lock()
	defer mu.Unlock()
	deadline := c.cfg.Now().Add(c.cfg.WriteTimeout)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.BinaryMessage, payload)
}

// pingLoop 按 Lark 要求的节奏定期发应用层 ping 帧。旧实现用 WebSocket 协议层
// PING（WriteControl）——SDK 源码证实 Lark 会无视它。
func (c *WSLongConnConnector) pingLoop(ctx context.Context, conn WSConn, writeMu *sync.Mutex, serviceID int32, interval time.Duration, log *slog.Logger, done chan<- struct{}) {
	defer close(done)
	if interval <= 0 {
		// 零/负间隔会无限滴答；安静地退出。连接时已记录过选定的间隔。
		<-ctx.Done()
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.writeFrame(writeMu, conn, NewPingFrame(serviceID)); err != nil {
				log.Warn("lark ws connector: ping write failed", "err", err.Error())
				// 别在这里拆连接——conn 挂掉时读循环会自己退出。在这里关会跟读
				// 循环自己的清理竞争。
				return
			}
		}
	}
}

// WSDialer 是本 connector 消费的拨号器面。*websocket.Dialer 直接满足它（经
// DialContext）；测试注入 fake。
type WSDialer interface {
	DialContext(ctx context.Context, urlStr string, requestHeader http.Header) (WSConn, *http.Response, error)
}

// WSConn 是本 connector 用到的 *websocket.Conn 子集。
type WSConn interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
	Close() error
}

// EndpointFetcher 解析每个安装的引导响应。实现负责 POST /callback/ws/endpoint
// 调用并浮出服务器推的 ClientConfig。
type EndpointFetcher interface {
	Endpoint(ctx context.Context, creds InstallationCredentials) (WSEndpoint, error)
}

// WSEndpoint 是解析出的传输目标，加上 connector 需要遵守的服务器推送运行时
// 配置（ping 节奏、重连提示）。ServiceID 从 wss URL 的 `service_id` 查询参数
// 解析出来——它标识我们的出站帧属于哪个 Lark 后端服务。
type WSEndpoint struct {
	URL               string
	Headers           http.Header
	ServiceID         int32
	PingInterval      time.Duration
	ReconnectInterval time.Duration
	ReconnectNonce    time.Duration
	ReconnectCount    int
}

// FrameDecoder 把数据帧的 JSON 负载变成 InboundMessage（ok=true）或空操作
// （ok=false）。connector 把 decoder 错误当逐帧处理：记日志并丢弃，不拆连接。
// decoder 收到的是 JSON 负载字节——外层二进制 Frame 信封已由 connector 剥掉。
type FrameDecoder interface {
	Decode(payload []byte, inst Installation) (msg InboundMessage, ok bool, err error)
}

// CredentialsProvider 为 connector 的 EndpointFetcher 调用提供明文
// InstallationCredentials。
type CredentialsProvider interface {
	Credentials(ctx context.Context, inst Installation) (InstallationCredentials, error)
}

// CredentialsProviderFunc 把自由函数适配成 CredentialsProvider。
type CredentialsProviderFunc func(ctx context.Context, inst Installation) (InstallationCredentials, error)

// Credentials 实现 CredentialsProvider。
func (f CredentialsProviderFunc) Credentials(ctx context.Context, inst Installation) (InstallationCredentials, error) {
	return f(ctx, inst)
}

// EndpointFetcherFunc 把普通函数适配成 EndpointFetcher。
type EndpointFetcherFunc func(ctx context.Context, creds InstallationCredentials) (WSEndpoint, error)

// Endpoint 实现 EndpointFetcher。
func (f EndpointFetcherFunc) Endpoint(ctx context.Context, creds InstallationCredentials) (WSEndpoint, error) {
	return f(ctx, creds)
}

// FrameDecoderFunc 把普通函数适配成 FrameDecoder。
type FrameDecoderFunc func(payload []byte, inst Installation) (InboundMessage, bool, error)

// Decode 实现 FrameDecoder。
func (f FrameDecoderFunc) Decode(payload []byte, inst Installation) (InboundMessage, bool, error) {
	return f(payload, inst)
}

// GorillaDialer 是生产环境的 WSDialer。
type GorillaDialer struct {
	Dialer *websocket.Dialer

	// Proxy 是 WebSocket 连接的代理函数。为 nil（零值）时拨号器默认用
	// http.ProxyFromEnvironment，尊重标准 HTTPS_PROXY / HTTP_PROXY / NO_PROXY
	// 环境变量。设为非 nil 函数可覆盖（例如自定义代理鉴权或固定代理 URL）。
	// 要完全禁用代理，传一个返回 (nil, nil) 的函数。
	Proxy func(*http.Request) (*url.URL, error)

	// ProxyURL 是 WebSocket 握手的可选固定 HTTP CONNECT 代理 URL。设置后优先于
	// Proxy。
	ProxyURL string
}

// NewGorillaDialer 构造带 15 秒握手超时默认值的生产拨号器。
func NewGorillaDialer() *GorillaDialer {
	return &GorillaDialer{
		Dialer: &websocket.Dialer{
			HandshakeTimeout: 15 * time.Second,
		},
	}
}

// DialContext 实现 WSDialer。
func (g *GorillaDialer) DialContext(ctx context.Context, urlStr string, requestHeader http.Header) (WSConn, *http.Response, error) {
	d := g.Dialer
	if d == nil {
		d = websocket.DefaultDialer
	}
	// 浅拷贝，避免改到共享拨号器的 Proxy 字段。
	dd := *d
	if g.ProxyURL != "" {
		proxyURL, err := url.Parse(g.ProxyURL)
		if err != nil {
			return nil, nil, fmt.Errorf("lark ws dialer: parse proxy url %q: %w", g.ProxyURL, err)
		}
		dd.Proxy = http.ProxyURL(proxyURL)
	} else if g.Proxy != nil {
		dd.Proxy = g.Proxy
	}
	if dd.Proxy == nil {
		dd.Proxy = http.ProxyFromEnvironment
	}
	c, resp, err := dd.DialContext(ctx, urlStr, requestHeader)
	if err != nil {
		return nil, resp, err
	}
	return c, resp, nil
}
