package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// errRuntimeSetChanged 在“守护的连接期间 runtime 集合发生变化”时返回，让外层循环
// 立刻用新的 runtime 集合重连而不是等退避。
var errRuntimeSetChanged = errors.New("runtime set changed")

const (
	taskWakeupMaxBackoff = 30 * time.Second

	// 带鉴权的控制连接承载的是 tasks.claim RPC 响应，不只是小小的唤醒提示。一条响应
	// 可含最多 32 个完整 Task 载荷，包括 agent 指令、项目/workspace 上下文、评论、
	// 资源与技能引用。旧的 64 KiB 上限比“一条合法的单任务响应”还小：服务器可能已
	// 提交一次 claim，而 daemon 会拒掉它的响应并正确地拒绝不安全的 HTTP 重复认领，
	// 结果任务被派发了却永远没启动。给读取留出界，同时为当前 batch 契约留下余量。
	taskWakeupReadLimit int64 = 64 << 20
)

var (
	taskWakeupPongWait          = 60 * time.Second
	taskWakeupWriteWait         = 10 * time.Second
	taskWakeupBackoffResetAfter = 10 * time.Second
)

// taskWakeup 是一次“去唤醒 ClaimTask 轮询器”的提示；runtimeID 为空表示任意 runtime。
type taskWakeup struct {
	runtimeID string
}

// taskWakeupLoop 维护与服务器的 task-wakeup WebSocket 长连接：连上后把新任务提示
// 经 taskWakeups 交给空闲的认领轮询器；断线按指数退避重连，HTTP 轮询兜底始终在跑。
func (d *Daemon) taskWakeupLoop(ctx context.Context, taskWakeups chan<- taskWakeup) {
	backoff := time.Second
	runtimeSetCh, unsub := d.runtimeSet.Subscribe()
	defer unsub()

	for {
		runtimeIDs := d.allRuntimeIDs()
		connectedFor, err := d.runTaskWakeupConnection(ctx, runtimeIDs, taskWakeups, runtimeSetCh)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRuntimeSetChanged) {
			// runtime 集合变了，立即带着新集合重连，退避归零。
			backoff = time.Second
			continue
		}
		if shouldResetTaskWakeupBackoff(connectedFor) {
			backoff = time.Second
		}
		if err != nil {
			d.logger.Debug("task wakeup websocket unavailable; polling fallback remains active", "error", err, "retry_in", backoff)
		}

		if err := sleepWithContextOrRuntimeChange(ctx, jitterDuration(backoff), runtimeSetCh); err != nil {
			return
		}
		if backoff < taskWakeupMaxBackoff {
			backoff *= 2
			if backoff > taskWakeupMaxBackoff {
				backoff = taskWakeupMaxBackoff
			}
		}
	}
}

// shouldResetTaskWakeupBackoff 判断一次连接维持了足够久（>= 阈值）就应把退避清零：
// 连上并稳定工作过说明网络没问题，下次失败从最小退避重新开始。
func shouldResetTaskWakeupBackoff(connectedFor time.Duration) bool {
	if connectedFor <= 0 {
		return false
	}
	return taskWakeupBackoffResetAfter <= 0 || connectedFor >= taskWakeupBackoffResetAfter
}

// jitterDuration 给退避时长叠加 ±1/5 的随机抖动，避免多台 daemon 失败后同步重试
// （惊群）。
func jitterDuration(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := d / 5
	if spread <= 0 {
		return d
	}
	delta := time.Duration(rand.Int63n(int64(spread)*2+1)) - spread
	return d + delta
}

// runTaskWakeupConnection 建立并维持一条到服务器 task-wakeup 端点的 WebSocket：
// 绑定心跳发送、WS RPC、读循环，直到 ctx 取消 / runtime 集合变化 / 读错误。
func (d *Daemon) runTaskWakeupConnection(ctx context.Context, runtimeIDs []string, taskWakeups chan<- taskWakeup, runtimeSetCh <-chan struct{}) (time.Duration, error) {
	wsURL, err := taskWakeupURL(d.cfg.ServerBaseURL, runtimeIDs)
	if err != nil {
		return 0, err
	}

	headers := http.Header{}
	if token := d.client.Token(); token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	if d.client.platform != "" {
		headers.Set("X-Client-Platform", d.client.platform)
	}
	if d.client.version != "" {
		headers.Set("X-Client-Version", d.client.version)
	}
	if d.client.os != "" {
		headers.Set("X-Client-OS", d.client.os)
	}
	// 宣告与 HTTP 路径相同的能力集，让基于这条 WS 连接做的 claim 拿到一致的能力
	// 闸门（MUL-4257）。
	headers.Set("X-Client-Capabilities", daemonClientCapabilities())

	// 手工构造的 websocket.Dialer 其 Proxy == nil，gorilla 视之为“直连”——与
	// websocket.DefaultDialer 不同，它不会回退到环境变量代理。在只能靠公司出口代理
	// 上网的机器上，握手必然失败，daemon 会悄悄降级成 HTTP 轮询。gorilla 在咨询
	// Proxy 之前会把 wss:// 改写为 https://，因此 HTTPS_PROXY 会作用到这次拨号；
	// 没设代理变量时 ProxyFromEnvironment 返回 nil，连接直连，和以前完全一样。
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return 0, err
	}
	connectedAt := time.Now()
	uptime := func() time.Duration { return time.Since(connectedAt) }
	defer conn.Close()
	// WS 一脱离，HTTP 心跳立刻恢复，避免“上一条连接的新鲜度窗口”在断线后继续压着
	// HTTP 心跳不发。
	defer d.clearWSHeartbeatAcks()

	d.logger.Info("task wakeup websocket connected", "runtimes", len(runtimeIDs))
	signalTaskWakeup(taskWakeups, "")
	// 连接恢复后唤醒持久化回传队列。
	d.signalTerminalReportReplay()
	// signalTaskWakeup 只唤醒空闲的 ClaimTask 轮询器。在途任务与 workspace 同步循环
	// 是挂在粗粒度 ticker（5s 与 30s）上、观察不到唤醒 channel 的，因此 WS 断线期间
	// 服务器改了什么——任务取消或 runtime 更新——在下一 tick 前对它们都不可见。
	// reconcile 广播会立刻把这些循环顶起来重查——包括 workspace 设置，同步循环在
	// reconcile 时对受管 workspace 重读设置，正是因为断线期间的一次设置编辑没留下任何
	// 提示（MUL-6921）。broadcast() 会对连续的调用去抖，反复横跳的连接不会放大成请求
	// 风暴。
	if d.reconcile != nil {
		d.reconcile.broadcast()
	}

	// 把所有写操作串行到单条 channel：gorilla/websocket 的 Conn 不允许并发
	// WriteMessage，而心跳发送方现在要与将来的“服务器发起的写”共存。缓冲区大小按
	// “整批每 runtime 心跳加余量”来定；固定 8 槽队列在 daemon 管理超过约 8 个 runtime
	// 时（一台机器连多个 workspace 时常见）会悄悄丢心跳，哪怕网络是健康的。
	writeBufSize := 16
	if 2*len(runtimeIDs) > writeBufSize {
		writeBufSize = 2 * len(runtimeIDs)
	}
	writes := make(chan *wsOutbound, writeBufSize)
	writerDone := make(chan struct{})
	go d.runWSWriter(conn, writes, writerDone)

	// 把通用 WS RPC 发送器（MUL-4257）挂到这条连接的写 channel 上。加锁保护，让“和
	// 拆除竞争的 Call”绝不往已关闭的 `writes` 上发：拆除在 close(writes) 之前先于
	// sendMu 下翻转 sendClosed，而发送器在整个非阻塞 send 期间持有 sendMu。
	var sendMu sync.Mutex
	sendClosed := false
	wsRPCGeneration := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if sendClosed {
			return nil, errWSRPCUnavailable
		}
		item := &wsOutbound{data: frame}
		select {
		case writes <- item:
			return item, nil
		default:
			return nil, errWSRPCWriteBufferFull
		}
	})
	// （重）连上的可能是刚升级的服务器：重新探测 batch claim 路由，而不是永远赖在旧
	// 兜底上（MUL-4257）。
	d.batchClaimUnsupported.Store(false)

	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		d.runWSHeartbeatSender(heartbeatCtx, runtimeIDs, writes)
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.readTaskWakeupMessagesForConnection(conn, taskWakeups, wsRPCGeneration)
	}()

	// 拆除时关闭 goroutine 必须按这个顺序：
	//   1. 取消心跳发送方的 ctx
	//   2. 等发送方真正返回——只有到这时，关 writes channel 才不会让
	//      sendWSHeartbeats 因“向已关闭的 channel 发送”而 panic
	//   3. 关闭 writes；writer 排空并退出
	//   4. 等 writer 结束，确保它不会活得比 conn 还久
	//
	// 按 defer 的 LIFO 顺序会先关 writes 再停发送方，所以把拆除折叠进单个 defer。
	defer func() {
		// 先关 socket，再失败掉挂起的 RPC。否则一条排队的 tasks.claim 帧仍会被冲刷到
		// 一条（在正常 runtimeSetCh 重连下）“还活着”的 socket 上——而那发生在
		// attach(nil) 已让 RPC 改走 HTTP 之后——服务器于是会在 HTTP 兜底之上再提交
		// 这次 WS claim，对同一批空闲槽重复认领（MUL-4257，Sol-Boy review）。这里把
		// conn 关掉后，runWSWriter 的下一次写会失败并*丢弃*整个队列，而不是送出它。
		conn.Close()
		// 摘掉 RPC（挂起请求失败 → HTTP 兜底，现在安全了，因为排队帧会被丢弃），并在
		// sendMu 下翻转 send-closed 标志，让任何在途的被保护发送先完成再关 writes。
		d.wsRPC.attach(nil)
		sendMu.Lock()
		sendClosed = true
		sendMu.Unlock()
		cancelHeartbeat()
		<-hbDone
		close(writes)
		<-writerDone
	}()

	select {
	case <-ctx.Done():
		return uptime(), ctx.Err()
	case <-runtimeSetCh:
		return uptime(), errRuntimeSetChanged
	case err := <-errCh:
		return uptime(), err
	}
}

// runWSWriter 把心跳发送方（以及将来任何 daemon 主动发的消息）的写操作收拢到单个
// goroutine。gorilla/websocket 要求所有 WriteMessage 调用来自同一 goroutine。
func (d *Daemon) runWSWriter(conn *websocket.Conn, writes <-chan *wsOutbound, done chan<- struct{}) {
	defer close(done)
	for item := range writes {
		// 跳过其 RPC 调用方已经放弃的帧：在兜底之后送出它们会造成重复认领
		// （MUL-4257）。beginWrite 同时把帧标记为已发，让并发的 cancel() 无法再收回它。
		if !item.beginWrite() {
			continue
		}
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, item.data); err != nil {
			d.logger.Debug("task wakeup websocket write failed", "error", err)
			conn.Close()
			// 排空剩余帧，让生产方在等 runTaskWakeupConnection 关 channel 时不会永远
			// 阻塞。
			for range writes {
			}
			return
		}
	}
}

// runWSHeartbeatSender 每个 HeartbeatInterval 为每个 runtime 发一条
// daemon:heartbeat。第一批立即发出，让服务器不必等满一个间隔就知道连接身份。帧排队
// 给 writer；队列满就丢掉这一拍（新鲜度窗口足够短，漏一拍只是意味着下一 tick 由 HTTP
// 顶上）。
func (d *Daemon) runWSHeartbeatSender(ctx context.Context, runtimeIDs []string, writes chan<- *wsOutbound) {
	d.sendWSHeartbeats(ctx, runtimeIDs, writes)
	interval := d.cfg.HeartbeatInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.sendWSHeartbeats(ctx, runtimeIDs, writes)
		}
	}
}

// sendWSHeartbeats 为每个 runtime 组一条心跳帧并排队发送。
func (d *Daemon) sendWSHeartbeats(ctx context.Context, runtimeIDs []string, writes chan<- *wsOutbound) {
	for _, rid := range runtimeIDs {
		if ctx.Err() != nil {
			return
		}
		frame, err := json.Marshal(protocol.Message{
			Type:    protocol.EventDaemonHeartbeat,
			Payload: marshalRaw(protocol.DaemonHeartbeatRequestPayload{RuntimeID: rid, SupportsBatchImport: true}),
		})
		if err != nil {
			d.logger.Debug("ws heartbeat marshal failed", "error", err, "runtime_id", rid)
			continue
		}
		select {
		case writes <- &wsOutbound{data: frame}:
		case <-ctx.Done():
			return
		default:
			// writer 堵了；丢这一拍。新鲜度窗口过期后，下一 tick HTTP 心跳会恢复。
			d.logger.Debug("ws heartbeat dropped: writer backlog", "runtime_id", rid)
		}
	}
}

// marshalRaw 序列化 v；失败返回 nil（调用方通常把 nil 载荷当“可忽略”处理）。
func marshalRaw(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// handleWSHeartbeatAck 分派一条经 WS task-wakeup 连接收到的 heartbeat_ack。从
// readTaskWakeupMessages 里拆出来，测试就能在无真实 WebSocket 的情况下演练分支逻辑。
//
// RuntimeGone=true 的 ack 是“HTTP 404 runtime not found”在 WebSocket 上的孪生：
// 它告诉 daemon 该 runtime 行已在服务端被删。我们把它接到与 HTTP 路径同一个自愈入口，
// 且*不*记录心跳新鲜度标记——假装 runtime 还活着，会让 HTTP 继续对那个已死的 UUID
// 跳过自己的心跳。
//
// handleRuntimeGone 的注册调用用的是 daemon 根 context，因此本函数可以安全地把任意
// 调用方 context 传进来。
func (d *Daemon) handleWSHeartbeatAck(ctx context.Context, ack *HeartbeatResponse) {
	d.handleWSHeartbeatAckForConnection(ctx, ack, d.wsRPC.currentGeneration())
}

// handleWSHeartbeatAckForConnection 是带“当前连接代号”的实现，防止把上一条连接
// 协商出的 rpc-v1 能力算到新连接头上。
func (d *Daemon) handleWSHeartbeatAckForConnection(ctx context.Context, ack *HeartbeatResponse, wsRPCGeneration uint64) {
	if ack == nil || ack.RuntimeID == "" {
		return
	}
	if ack.RuntimeGone {
		go d.handleRuntimeGone(ack.RuntimeID)
		return
	}
	for _, capability := range ack.ServerCapabilities {
		if capability == protocol.DaemonCapabilityRPCV1 {
			d.wsRPC.markRPCV1Supported(wsRPCGeneration)
			break
		}
	}
	d.recordWSHeartbeatAck(ack.RuntimeID)
	d.handleHeartbeatActions(ctx, ack.RuntimeID, ack)
}

// readTaskWakeupMessages 在 conn 上循环读帧并分派（供既有调用点使用）。
func (d *Daemon) readTaskWakeupMessages(conn *websocket.Conn, taskWakeups chan<- taskWakeup) error {
	return d.readTaskWakeupMessagesForConnection(conn, taskWakeups, d.wsRPC.currentGeneration())
}

// readTaskWakeupMessagesForConnection 在 conn 上循环读帧并按类型分派：任务可用提示、
// runtime profile/workspace 变更、待办提示、心跳 ack 与 WS RPC 响应。
func (d *Daemon) readTaskWakeupMessagesForConnection(conn *websocket.Conn, taskWakeups chan<- taskWakeup, wsRPCGeneration uint64) error {
	d.configureTaskWakeupReadLiveness(conn)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if err := d.extendTaskWakeupReadDeadline(conn); err != nil {
			return err
		}
		var msg protocol.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			d.logger.Debug("task wakeup websocket invalid message", "error", err)
			continue
		}
		switch msg.Type {
		case protocol.EventDaemonTaskAvailable:
			var payload protocol.TaskAvailablePayload
			if len(msg.Payload) > 0 {
				if err := json.Unmarshal(msg.Payload, &payload); err != nil {
					d.logger.Debug("task wakeup websocket invalid payload", "error", err)
					continue
				}
			}
			if payload.RuntimeID != "" {
				d.logger.Debug("task wakeup received", "runtime_id", payload.RuntimeID, "task_id", payload.TaskID)
			}
			signalTaskWakeup(taskWakeups, payload.RuntimeID)
		case protocol.EventDaemonRuntimeProfilesChanged:
			var payload protocol.RuntimeProfilesChangedPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				d.logger.Debug("runtime profile refresh websocket invalid payload", "error", err)
				continue
			}
			if payload.WorkspaceID == "" {
				d.logger.Debug("runtime profile refresh websocket missing workspace_id")
				continue
			}
			go d.handleRuntimeProfilesChanged(payload)
		case protocol.EventDaemonWorkspacesChanged:
			if d.workspaceChanges != nil {
				d.workspaceChanges.broadcast()
			}
		case protocol.EventDaemonPendingWork:
			var payload protocol.PendingWorkPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				d.logger.Debug("pending work websocket invalid payload", "error", err)
				continue
			}
			if payload.RuntimeID == "" {
				d.logger.Debug("pending work websocket missing runtime_id")
				continue
			}
			// 独立 goroutine：该提示会触发一次 HTTP 心跳加上它所认领的工作，读泵必须
			// 腾出来接下一帧。
			go d.handlePendingWorkHint(payload.RuntimeID, payload.Kind)
		case protocol.EventDaemonHeartbeatAck:
			var ack HeartbeatResponse
			if err := json.Unmarshal(msg.Payload, &ack); err != nil {
				d.logger.Debug("ws heartbeat ack invalid payload", "error", err)
				continue
			}
			d.handleWSHeartbeatAckForConnection(context.Background(), &ack, wsRPCGeneration)
		case protocol.EventDaemonRPCResponse:
			var resp protocol.RPCResponsePayload
			if err := json.Unmarshal(msg.Payload, &resp); err != nil {
				d.logger.Debug("ws rpc response invalid payload", "error", err)
				continue
			}
			d.wsRPC.deliver(resp)
		}
	}
}

// configureTaskWakeupReadLiveness 设置读限制、读写截止时间与 ping/pong 处理器，让
// 半死连接能被及时判死。
func (d *Daemon) configureTaskWakeupReadLiveness(conn *websocket.Conn) {
	conn.SetReadLimit(taskWakeupReadLimit)
	if err := d.extendTaskWakeupReadDeadline(conn); err != nil {
		d.logger.Debug("task wakeup websocket read deadline failed", "error", err)
	}
	conn.SetPongHandler(func(string) error {
		return d.extendTaskWakeupReadDeadline(conn)
	})
	conn.SetPingHandler(func(appData string) error {
		if err := d.extendTaskWakeupReadDeadline(conn); err != nil {
			return err
		}
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(taskWakeupWriteWait))
	})
}

// extendTaskWakeupReadDeadline 把读截止时间顺延一个 pongWait，表示“连接仍活着”。
func (d *Daemon) extendTaskWakeupReadDeadline(conn *websocket.Conn) error {
	return conn.SetReadDeadline(time.Now().Add(taskWakeupPongWait))
}

// handleRuntimeProfilesChanged 收到 runtime profile 变更提示后刷新该 workspace 的
// runtime profile。
func (d *Daemon) handleRuntimeProfilesChanged(payload protocol.RuntimeProfilesChangedPayload) {
	if payload.WorkspaceID == "" {
		return
	}
	if err := d.refreshWorkspaceRuntimeProfiles(d.recoveryContext(), payload.WorkspaceID); err != nil {
		d.logger.Debug("runtime profile refresh websocket hint failed",
			"workspace_id", payload.WorkspaceID,
			"runtime_profile_id", payload.RuntimeProfileID,
			"error", err)
	}
}

// signalTaskWakeup 尝试投递一次唤醒提示；接收方忙（channel 满）就静默丢弃——认领
// 轮询器本来就有自己的轮询节奏，错过这次提示只会迟一个周期。
func signalTaskWakeup(taskWakeups chan<- taskWakeup, runtimeID string) {
	select {
	case taskWakeups <- taskWakeup{runtimeID: runtimeID}:
	default:
	}
}

// taskWakeupURL 把 daemon 服务器基地址改写成带 runtime_ids 查询参数的 task-wakeup
// WebSocket 地址。
func taskWakeupURL(baseURL string, runtimeIDs []string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("invalid daemon server URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("daemon server URL must use http, https, ws, or wss")
	}

	u.Path = strings.TrimRight(u.Path, "/") + "/api/daemon/ws"
	u.RawPath = ""
	q := u.Query()
	ids := append([]string(nil), runtimeIDs...)
	sort.Strings(ids)
	if len(ids) > 0 {
		q.Set("runtime_ids", strings.Join(ids, ","))
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

// sleepWithContextOrRuntimeChange 睡眠 d 时长，但 ctx 取消或 runtime 集合变化都会
// 提前唤醒。
func sleepWithContextOrRuntimeChange(ctx context.Context, d time.Duration, runtimeSetCh <-chan struct{}) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-runtimeSetCh:
		return nil
	case <-timer.C:
		return nil
	}
}
