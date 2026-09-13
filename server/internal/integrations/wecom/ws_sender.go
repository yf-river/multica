// ws_sender.go —— 一条 WebSocket 连接的串行化写入器。gorilla 禁止并发写，所以
// 每条出站帧都过同一把互斥锁；ping 循环、订阅握手、Send() 调用共用这个写入器。
package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsConn 是 wecom 包用到的那一部分 gorilla Conn。收得尽量小，测试就能注入假实
// 现，而不用内嵌 gorilla 的全部接口面。
type wsConn interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(messageType int, data []byte) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
	Close() error
}

// Dialer 打开一条到 aibot 端点的 WebSocket 连接。生产用 gorilla 默认拨号器；
// 测试接一只指向 httptest.Server 的假拨号器。
type Dialer interface {
	DialContext(ctx context.Context, url string, header http.Header) (wsConn, *http.Response, error)
}

// defaultDialer 是生产用的拨号器。Proxy 要显式设置，因为零值 websocket.Dialer
// 的 Proxy 是 nil、会无视环境变量——这一点和 websocket.DefaultDialer 不同——
// 而自托管部署若在公司的出口代理后面，只能经 HTTPS_PROXY 触达企微端点。
var defaultDialer Dialer = gorillaDialer{d: &websocket.Dialer{
	HandshakeTimeout: handshakeTimeout,
	Proxy:            http.ProxyFromEnvironment,
}}

type gorillaDialer struct {
	d *websocket.Dialer
}

func (g gorillaDialer) DialContext(ctx context.Context, u string, header http.Header) (wsConn, *http.Response, error) {
	conn, resp, err := g.d.DialContext(ctx, u, header)
	if err != nil {
		return nil, resp, err
	}
	return &gorillaWSConn{Conn: conn}, resp, nil
}

// gorillaWSConn 包一层 *websocket.Conn，让它满足 wsConn、又不把具体类型泄漏进
// wsConn 的方法签名。
type gorillaWSConn struct {
	*websocket.Conn
}

// wsSender 串行化对一条 WebSocket 连接的写。每次 Connect() 调用实例化一个，
// 连接结束时丢弃。
type wsSender struct {
	conn wsConn
	mu   sync.Mutex
	log  *slog.Logger

	// replies 装着正在等服务端裁定的调用方，按它们写出的 req_id 作键。只有读
	// 取循环会给这些 channel 投递，这正是入站回调不能跑在读取循环上的原因——
	// 见 sendTextCtx 上的说明。
	ackMu   sync.Mutex
	replies map[string]*replyWaiter

	// seq 按「到达 socket 的顺序」给出站帧编号。受 mu 保护，所以它就是线上顺序
	// （构造保证），也是把一次被追踪的发送尝试与其结局配对的东西——req_id 干不
	// 了这个活，因为 pong 会回显服务端的 req_id，它可能为空或重复。seq 永不上线。
	seq uint64
}

func newWSSender(conn wsConn, log *slog.Logger) *wsSender {
	if log == nil {
		log = slog.Default()
	}
	return &wsSender{conn: conn, log: log, replies: make(map[string]*replyWaiter)}
}

// ackTimeout 限定等待一次裁定的时长。企微通常几百毫秒内回；超过它，我们宁可
// 假设 ack 丢了、而不是帧被拒——因为两种假设要求完全相反的处理。
const ackTimeout = 5 * time.Second

// errAckTimeout —— 帧已经发出，却没有裁定回来。它区别于「被拒」：消息很可能已
// 送达，所以调用方若重试要自担风险，而不是直接报失败。
var errAckTimeout = errors.New("wecom: timed out waiting for the server verdict")

// wecomAPIError 是服务端明确说出的拒绝。携带 errcode 而不是一段字符串，让调用
// 方能把「永久性拒绝」（坏帧、机器人已被移出会话）和「临时性拒绝」（被限流）
// 区分开，而不是靠对文字做模式匹配。
type wecomAPIError struct {
	Cmd  string
	Code int
	Msg  string
}

func (e *wecomAPIError) Error() string {
	return fmt.Sprintf("wecom: %s rejected errcode=%d errmsg=%s", e.Cmd, e.Code, e.Msg)
}

// replyWaiter 是一个挂在某个 req_id 上等待的调用方。
type replyWaiter struct{ ch chan replyResult }

// replyResult 是一次服务端应答。只带裁定的 ack 其 body 为 nil。
type replyResult struct {
	code int
	msg  string
	body json.RawMessage
}

// routeResponse 把一条服务端响应交给在等它的人，并报告是否确实有人在等。读取
// 循环对每条「应答我们某次写出」的帧都调它；没有认领者的 ack 不算错误，因为不
// 等待回复的推送与请求共用这条连接。
func (s *wsSender) routeResponse(env frameEnvelope) bool {
	return s.deliverReply(env)
}

// awaitReply 登记对「即将写出的这帧」的响应感兴趣。false 表示这个 req_id 已经
// 有人占了——对新造的 id 来说这是碰撞，宁可当场失败也不悄悄串线。
func (s *wsSender) awaitReply(reqID string) (*replyWaiter, bool) {
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	if _, taken := s.replies[reqID]; taken {
		return nil, false
	}
	w := &replyWaiter{ch: make(chan replyResult, 1)}
	s.replies[reqID] = w
	return w, true
}

// cancelReply 让一个 waiter 退役。所有退出路径都调它，包括成功那条——一个请求
// 就是一帧一答，登记项永远不会用第二次；留着它等于每次发送都漏一项。
func (s *wsSender) cancelReply(reqID string, w *replyWaiter) {
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	if cur, ok := s.replies[reqID]; ok && cur == w {
		delete(s.replies, reqID)
	}
}

// deliverReply 把响应交给问它的那个请求（如果有），并报告是否已被取走。
func (s *wsSender) deliverReply(env frameEnvelope) bool {
	if env.Headers.ReqID == "" {
		return false
	}
	s.ackMu.Lock()
	w, ok := s.replies[env.Headers.ReqID]
	if ok {
		delete(s.replies, env.Headers.ReqID)
	}
	s.ackMu.Unlock()
	if !ok {
		return false
	}
	// 带缓冲的 channel，且上面已删除登记项，所以这里绝不会阻塞、也绝不会送两遍。
	select {
	case w.ch <- replyResult{code: env.ErrCode, msg: env.ErrMsg, body: env.Body}:
	default:
	}
	return true
}

// request 以自造的 req_id 写一帧，并等待完整应答。非 nil 错误要么是携带服务端
// errcode 的 *wecomAPIError、errAckTimeout、要么是一次传输层失败。
func (s *wsSender) request(ctx context.Context, cmd string, body map[string]any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reqID := newReqID()
	w, ok := s.awaitReply(reqID)
	if !ok {
		return nil, fmt.Errorf("wecom: %s req_id %s is already awaiting a response", cmd, reqID)
	}
	defer s.cancelReply(reqID, w)

	if err := s.write(map[string]any{
		"cmd":     cmd,
		"headers": frameHeaders{ReqID: reqID},
		"body":    body,
	}); err != nil {
		return nil, err
	}

	timer := time.NewTimer(ackTimeout)
	defer timer.Stop()
	select {
	case res := <-w.ch:
		if res.code != 0 {
			return nil, &wecomAPIError{Cmd: cmd, Code: res.code, Msg: res.msg}
		}
		return res.body, nil
	case <-timer.C:
		return nil, errAckTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// write 把 frame 序列化成 JSON，并在 writer 互斥锁内发出。调用方不得持有
// wecomChannel 上的 sendMu——这里没有任何往 Channel 里回头的东西。
func (s *wsSender) write(frame map[string]any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("wecom: marshal frame: %w", err)
	}
	// 字段抽取放在锁外、落日志放锁内。这把锁正是 ping 循环、agent 回复与收件箱
	// 推送「变有序」的临界点，所以锁内采的记录与线上顺序是构造保证一致；锁外采
	// 的只是与它相关——一个 goroutine 可能先打出自己的日志行、还没拿到锁就被换
	// 下调度器，于是日志把错误的那帧点名为第一。抽取是开销大的那一半（对消息正
	// 文做一遍正则脱敏 + 逐 rune 裁剪），不需要这种保证，所以留在外面；锁内跑的
	// 只是：关闭追踪时一次 nil 判断，开启时两条日志行，外加本来就同区段的 socket
	// 写。
	t := traceOutFields(s.log, frame)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	traceOutAttempt(s.log, s.seq, t)

	stage := traceStageDeadline
	attempted := false
	err = s.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	if err == nil {
		stage = traceStageWrite
		attempted = true
		err = s.conn.WriteMessage(websocket.TextMessage, payload)
	}
	traceOutResult(s.log, s.seq, t, stage, err)
	if err != nil && attempted {
		return fmt.Errorf("%w: %w", errWriteAttempted, err)
	}
	return err
}

// errWriteAttempted 标记一次「由 socket 写本身发起」的失败，区别于「任何字节都
// 还没能出去」就失败的（marshal 错误、连接拒绝设 deadline）。
//
// 这个区分属于调用方，不属于本函数。一旦进入 WriteMessage，帧可能已到达对端、
// 且在本地浮出失败之前就在 TCP 层被确认了——半关连接会向写方报 "broken pipe"，
// 而读者手上其实已有那些字节。所以过了这一点的失败不是「未送达」的证据；把它
// 当未送达处理的调用方，要么否认了一次确实发生的送达，要么把企微已经处理过的
// 帧重发一遍。
//
// 写之前的失败都不带这个标记：那些是确凿的本地失败，调用方可以放心上报为确凿。
var errWriteAttempted = errors.New("wecom: frame write attempted")

// sendText 向某个指定会话推一条纯文本的 aibot_send_msg（主动推送）。调用方传
// channel.ChatType，让 aibot 的 chat_type 整数（1=单聊、2=群聊）在 wecom 这一
// 侧的边界上决定，而不是由引擎决定。OutboundReplier 与 Outbound 使用它。
func (s *wsSender) sendText(chatID string, chatTypeInt int, content string) error {
	return s.sendTextCtx(context.Background(), chatID, chatTypeInt, content)
}

// sendTextCtx 是读取服务端裁定的 sendText。在那之前，推送是发完即忘的：一条被
// 企微拒掉的帧——超了大小上限、发往机器人已不在的会话、被限流——返回 nil，于是
// 调用方记下了一次从未发生的送达，运维只看到一串没有归属的 ack 行飘过。
//
// 这里可以放心阻塞，仅仅因为入站回调已不再跑在读取循环上（wecom_channel.go）：
// 读取循环是 ack 的唯一投递者，一个在回调内部等待 ack 的发送会等在自己身上。
func (s *wsSender) sendTextCtx(ctx context.Context, chatID string, chatTypeInt int, content string) error {
	body, err := sendMsgTextBody(chatID, chatTypeInt, content)
	if err != nil {
		return err
	}
	_, err = s.request(ctx, cmdSendMsg, body)
	return err
}
