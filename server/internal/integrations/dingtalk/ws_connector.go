// ws_connector.go：手写钉钉 Stream 模式的 WebSocket 连接，替代厂商 Stream SDK。
//
// 它镜像飞书的 ws_connector：单个阻塞的 run() 只拥有恰好一条 socket 会话。
// 重连/退避/租约都在共享的 engine.Supervisor 里，所以 run() 只需返回——正常收
// 关闭返回 nil、连接断了返回错误——由 supervisor 决定何时重拨。
//
// 帧处理：网关每 ~30 秒发一次 SYSTEM ping，这里回 pong；网关要求重连时发
// SYSTEM disconnect，run 干净地返回 nil 让 supervisor 重拨；真正的业务帧是
// CALLBACK 类型的机器人消息，交给 onMessage 并立即回 ack。
package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	streamPingInterval = 30 * time.Second
	streamReadDeadline = 90 * time.Second
	streamWriteTimeout = 10 * time.Second
)

// wsConn 是连接器实际用到的 *websocket.Conn 的那一小片接口，抽出来是为了测试时
// 能注入一块假的内存 socket。
type wsConn interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	WriteControl(messageType int, data []byte, deadline time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
	SetPongHandler(h func(appData string) error)
	Close() error
}

// wsDialer 为某个拨号 URL 打开一条 wsConn。生产实现包着 gorilla；测试注入假的。
type wsDialer interface {
	dial(ctx context.Context, dialURL string) (wsConn, error)
}

type gorillaDialer struct{}

func (gorillaDialer) dial(ctx context.Context, dialURL string) (wsConn, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, dialURL, nil)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// wsConnector 跑一条安装的 Stream 会话。
type wsConnector struct {
	httpClient *http.Client
	apiBase    string
	appKey     string
	appSecret  string

	dialer    wsDialer
	open      func(ctx context.Context) (string, error) // 取拨号 URL；缺省走 openConnection
	onMessage func(ctx context.Context, data *botCallbackData) error
	logger    *slog.Logger

	pingInterval time.Duration
	readDeadline time.Duration
	writeTimeout time.Duration
}

func (c *wsConnector) withDefaults() {
	if c.dialer == nil {
		c.dialer = gorillaDialer{}
	}
	if c.open == nil {
		c.open = func(ctx context.Context) (string, error) {
			return openConnection(ctx, c.httpClient, c.apiBase, c.appKey, c.appSecret)
		}
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.pingInterval == 0 {
		c.pingInterval = streamPingInterval
	}
	if c.readDeadline == 0 {
		c.readDeadline = streamReadDeadline
	}
	if c.writeTimeout == 0 {
		c.writeTimeout = streamWriteTimeout
	}
}

// run 打开连接并持续服务帧，直到 ctx 被取消（返回 nil）、网关发 SYSTEM
// disconnect（返回 nil）、或 socket 断开（返回错误，让 supervisor 退避后重连）。
func (c *wsConnector) run(ctx context.Context) error {
	c.withDefaults()

	dialURL, err := c.open(ctx)
	if err != nil {
		return fmt.Errorf("dingtalk stream: open connection: %w", err)
	}
	conn, err := c.dialer.dial(ctx, dialURL)
	if err != nil {
		// dialURL 带着一次性 Stream ticket。拨号错误可能把整条 URL 原样回显出来，
		// 所以这里不把原始错误包进 supervisor/日志链路（避免把 ticket 泄露到日志）。
		return fmt.Errorf("dingtalk stream: dial failed (%T)", err)
	}

	runCtx, cancel := context.WithCancel(ctx)

	var closeOnce sync.Once
	closeConn := func() { closeOnce.Do(func() { _ = conn.Close() }) }

	var writeMu sync.Mutex
	writeResponse := func(resp dataFrameResponse) error {
		b, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))
		return conn.WriteMessage(websocket.TextMessage, b)
	}

	done := make(chan struct{})
	// 看门狗：gorilla 的 ReadMessage 不感知 ctx，所以 run ctx 一旦被取消，只有关闭
	// socket 才能把卡在系统调用里的读给唤醒回来。
	go func() {
		select {
		case <-runCtx.Done():
			closeConn()
		case <-done:
		}
	}()

	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(c.pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				writeMu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(c.writeTimeout))
				writeMu.Unlock()
				if err != nil {
					return // 拆解由读循环负责
				}
			}
		}
	}()

	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(c.readDeadline))
	})

	defer func() {
		cancel()
		closeConn()
		close(done)
		<-pingDone
	}()

	for {
		if err := conn.SetReadDeadline(time.Now().Add(c.readDeadline)); err != nil {
			return fmt.Errorf("dingtalk stream: set read deadline: %w", err)
		}
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			if runCtx.Err() != nil {
				return nil // ctx cancelled → graceful stop
			}
			return fmt.Errorf("dingtalk stream: read: %w", err)
		}
		if msgType != websocket.TextMessage {
			continue
		}
		var frame dataFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			c.logger.WarnContext(runCtx, "dingtalk stream: malformed frame", "error", err)
			continue
		}

		switch {
		case frame.Type == frameTypeSystem && frame.topic() == systemTopicPing:
			if err := writeResponse(newPongResponse(frame.messageID(), frame.Data)); err != nil {
				if runCtx.Err() != nil {
					return nil
				}
				return fmt.Errorf("dingtalk stream: write pong: %w", err)
			}
		case frame.Type == frameTypeSystem && frame.topic() == systemTopicDisconnect:
			// 网关请我们重连；干净地返回 nil，让 supervisor 去重拨。
			return nil
		case frame.Type == frameTypeCallback && frame.topic() == botMessageTopic:
			c.dispatchCallback(runCtx, &frame, writeResponse)
		default:
			c.logger.WarnContext(runCtx, "dingtalk stream: unhandled frame",
				"type", frame.Type, "topic", frame.topic())
		}
	}
}

// dispatchCallback 解析一条机器人消息回调，交给 onMessage，并且无论如何都回 ack
// （回显该帧的 messageId）。解析或处理出错只记日志、绝不向上抛：钉钉对未 ack 的帧
// 过期很快，引擎侧 (installation, msgId) 的去重会兜住任何重投——这与原 SDK 回调
// 「永远 ack」的行为保持一致。
func (c *wsConnector) dispatchCallback(ctx context.Context, frame *dataFrame, writeResponse func(dataFrameResponse) error) {
	var payload botCallbackData
	if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
		c.logger.WarnContext(ctx, "dingtalk stream: decode callback", "error", err)
	} else if err := c.onMessage(ctx, &payload); err != nil {
		c.logger.WarnContext(ctx, "dingtalk stream: handler error", "error", err)
	}
	if err := writeResponse(newAckResponse(frame.messageID())); err != nil {
		c.logger.WarnContext(ctx, "dingtalk stream: write ack", "error", err)
	}
}
