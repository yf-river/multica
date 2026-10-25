// Package daemonws 实现服务器侧承载“daemon WebSocket 连接”的 hub。
//
// 角色定位：daemon（本机 agent 运行时）会与服务器建立一条 WS 长连接（task-wakeup /
// 控制连接）。hub 负责按 runtime ID / workspace ID / user ID 给这些连接建立索引，
// 把“新任务来了”“runtime profile 变了”“workspace 变了”“有待办工作”之类的轻量
// 唤醒提示（best-effort）推给相关 daemon，并处理 daemon 发来的 heartbeat 与通用 RPC
// 请求。正确性仍由 HTTP claim 保证——WS 提示丢了 daemon 也能靠自己的轮询/心跳节奏
// 兜底，所以这里的一切都是“尽力而为的加速”。
package daemonws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

// ClientIdentity 捕捉已鉴权的 daemon 连接的作用域。
type ClientIdentity struct {
	DaemonTokenHash    string
	DaemonTokenOwnerID string
	DaemonID           string
	UserID             string
	// WorkspaceID 是老调用方与 daemon-token 鉴权使用的“单 workspace”旧作用域。新代码应
	// 从“该连接授权的 runtime 行”填 WorkspaceIDs。
	WorkspaceID   string
	WorkspaceIDs  []string
	RuntimeIDs    []string
	ClientVersion string
	// Capabilities 是连接时捕获的原始 X-Client-Capabilities 头，让 RPC handler 能遵守
	// 与 HTTP 路径一致的能力闸门。
	Capabilities string
}

// AuthorizedWorkspaceIDs 以稳定顺序返回连接的 workspace 作用域：优先多 workspace 字段，
// 对老测试/调用方回退到 WorkspaceID。
func (i ClientIdentity) AuthorizedWorkspaceIDs() []string {
	seen := make(map[string]struct{}, len(i.WorkspaceIDs)+1)
	out := make([]string, 0, len(i.WorkspaceIDs)+1)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range i.WorkspaceIDs {
		add(id)
	}
	if len(out) == 0 {
		add(i.WorkspaceID)
	}
	return out
}

// PrimaryWorkspaceID 返回主 workspace（授权列表的第一个；空则空串）。
func (i ClientIdentity) PrimaryWorkspaceID() string {
	ids := i.AuthorizedWorkspaceIDs()
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// AllowsWorkspace 报告 workspaceID 是否落在连接作用域内。空作用域保持“放行”，兼容那些
// 不带 workspace 数据直接构造 ClientIdentity 的旧版单元测试。
func (i ClientIdentity) AllowsWorkspace(workspaceID string) bool {
	ids := i.AuthorizedWorkspaceIDs()
	if len(ids) == 0 {
		return true
	}
	for _, id := range ids {
		if id == workspaceID {
			return true
		}
	}
	return false
}

// client 是一条已建立的 daemon WS 连接及其运行状态。
type client struct {
	hub      *Hub
	conn     *websocket.Conn
	send     chan []byte
	identity ClientIdentity
	runtimes map[string]struct{}

	// ctx 在连接拆除时被取消，让异步 RPC handler 停下而不是对着死 socket 干活。
	// cancel 在 readPump 的 defer 里调用。
	ctx    context.Context
	cancel context.CancelFunc

	// sendMu 保护 sendClosed：迟到的异步发送（如 RPC 响应 goroutine）绝不写已关闭的
	// send channel。teardown 在 close(send) 之前于 sendMu 下翻转 sendClosed。
	sendMu     sync.Mutex
	sendClosed bool

	// 事件去重：markSeen 用 seenIDs 查重、seenList 做 FIFO 淘汰，防止带 event id 的
	// 提示被重复投递。
	dedupMu  sync.Mutex
	seenIDs  map[string]struct{}
	seenList []string

	// rpcSem 限制该连接的并发 RPC handler 数。
	rpcSem chan struct{}
}

// trySend 把 frame 交给写泵，不阻塞、绝不写已关闭的 channel（对并发拆除安全）。
// 缓冲满或连接关闭中返回 false。
func (c *client) trySend(frame []byte) bool {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.sendClosed {
		return false
	}
	select {
	case c.send <- frame:
		return true
	default:
		return false
	}
}

const eventDedupCapacity = 128

// markSeen 把 eventID 记为已投递给本客户端。空 event id 关闭去重、总是投递；返回
// false 表示该 id 已见过、应跳过。
func (c *client) markSeen(eventID string) bool {
	if eventID == "" {
		return true
	}
	c.dedupMu.Lock()
	defer c.dedupMu.Unlock()
	if c.seenIDs == nil {
		c.seenIDs = make(map[string]struct{}, eventDedupCapacity)
	}
	if _, ok := c.seenIDs[eventID]; ok {
		return false
	}
	c.seenIDs[eventID] = struct{}{}
	c.seenList = append(c.seenList, eventID)
	if len(c.seenList) > eventDedupCapacity {
		drop := c.seenList[0]
		c.seenList = c.seenList[1:]
		delete(c.seenIDs, drop)
	}
	return true
}

// HeartbeatHandler 处理一条 daemon:heartbeat 帧。它必须校验 runtimeID 属于
// identity.RuntimeIDs（连接鉴权过的作用域），并返回要发回的 ack 载荷。返回错误会跳过
// ack 并以 debug 级别记日志。
type HeartbeatHandler func(ctx context.Context, identity ClientIdentity, runtimeID string, supportsBatchImport bool) (*protocol.DaemonHeartbeatAckPayload, error)

// RPCHandler 处理一条通用 daemon:rpc_request（MUL-4257）。它按 method（如
// "tasks.claim"）分派，把工作限定在 identity（DaemonID + 鉴权过的 RuntimeIDs）作用域
// 内，返回 HTTP 风格状态码加响应体、或一个错误。返回的错误会作为非 2xx RPC 响应露给
// daemon，让它回退 HTTP。handler 跑在自己的 goroutine 里，所以它不能假设自己独占读泵。
type RPCHandler func(ctx context.Context, identity ClientIdentity, method string, body json.RawMessage) (status int, respBody json.RawMessage, err error)

// maxInFlightRPCPerClient 限制每条连接的并发 RPC handler 数，防止单个 daemon 通过
// 一条 socket 扇出无界的 goroutine / DB 工作。
const maxInFlightRPCPerClient = 8

// MessageKindRecorder 是可选指标钩子：每条入站 daemon WebSocket 帧恰好调用一次。
// kind 是去掉 "daemon:" 前缀后的协议消息类型（如 "heartbeat"）；对未建模类型为字面量
// "unknown"。nil recorder 安全地变成无操作。
type MessageKindRecorder interface {
	RecordDaemonWSMessageReceived(kind string)
}

// Hub 按 runtime ID 索引 daemon WebSocket 连接。消息是尽力而为的唤醒提示；正确性仍靠
// daemon 走 HTTP claim。
type Hub struct {
	upgrader websocket.Upgrader

	mu          sync.RWMutex
	clients     map[*client]bool
	byRuntime   map[string]map[*client]bool
	byWorkspace map[string]map[*client]bool
	byUser      map[string]map[*client]bool

	hbMu        sync.RWMutex
	onHeartbeat HeartbeatHandler

	rpcMu sync.RWMutex
	onRPC RPCHandler

	kindMu       sync.RWMutex
	kindRecorder MessageKindRecorder
}

// NewHub 创建一个空 hub。
func NewHub() *Hub {
	return &Hub{
		upgrader: websocket.Upgrader{
			// daemon 客户端在升级前用 Authorization 头鉴权。浏览器经原生 WS API 无法设
			// 这些头，而 DaemonAuth 不收 cookie，因此 cookie 型 CSWSH 对本端点不适用。
			// 若 DaemonAuth 将来增加 cookie 回退，要重新评估这里。
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:     make(map[*client]bool),
		byRuntime:   make(map[string]map[*client]bool),
		byWorkspace: make(map[string]map[*client]bool),
		byUser:      make(map[string]map[*client]bool),
	}
}

// SetHeartbeatHandler 安装 daemon:heartbeat 帧用的回调。在 handler 构造完成后才接线，
// 因为 handler 依赖的 DB 查询在建 hub 时还没有。nil handler 禁用 WS 心跳处理——daemon
// 会透明回退 HTTP 心跳，因为它的兜底定时器在没有 ack 到达时就会触发。
func (h *Hub) SetHeartbeatHandler(fn HeartbeatHandler) {
	if h == nil {
		return
	}
	h.hbMu.Lock()
	h.onHeartbeat = fn
	h.hbMu.Unlock()
}

func (h *Hub) heartbeatHandler() HeartbeatHandler {
	h.hbMu.RLock()
	defer h.hbMu.RUnlock()
	return h.onHeartbeat
}

// SetRPCHandler 安装 daemon:rpc_request 帧用的回调（MUL-4257）。与 SetHeartbeatHandler
// 一样在 handler 构造后接线。nil handler 禁用 WS RPC——daemon 回退到 HTTP claim 端点。
func (h *Hub) SetRPCHandler(fn RPCHandler) {
	if h == nil {
		return
	}
	h.rpcMu.Lock()
	h.onRPC = fn
	h.rpcMu.Unlock()
}

func (h *Hub) rpcHandler() RPCHandler {
	h.rpcMu.RLock()
	defer h.rpcMu.RUnlock()
	return h.onRPC
}

// SetMessageKindRecorder 安装可选回调：每条入站 daemon WebSocket 帧恰好触发一次。
// 供指标层按 handler 类型统计流量，同时不与任何具体采集器硬耦合。
func (h *Hub) SetMessageKindRecorder(rec MessageKindRecorder) {
	if h == nil {
		return
	}
	h.kindMu.Lock()
	h.kindRecorder = rec
	h.kindMu.Unlock()
}

func (h *Hub) messageKindRecorder() MessageKindRecorder {
	if h == nil {
		return nil
	}
	h.kindMu.RLock()
	defer h.kindMu.RUnlock()
	return h.kindRecorder
}

// HandleWebSocket 处理一次 WS 握手并启动读写泵。identity 携带已鉴权的作用域；没有
// runtime 也没有 user 身份的直接拒绝。
func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request, identity ClientIdentity) {
	if len(identity.RuntimeIDs) == 0 && identity.UserID == "" {
		http.Error(w, `{"error":"runtime_ids or user identity required"}`, http.StatusBadRequest)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("daemon websocket upgrade failed", "error", err)
		return
	}

	runtimes := make(map[string]struct{}, len(identity.RuntimeIDs))
	for _, runtimeID := range identity.RuntimeIDs {
		if runtimeID != "" {
			runtimes[runtimeID] = struct{}{}
		}
	}
	c := &client{
		hub:      h,
		conn:     conn,
		send:     make(chan []byte, 16),
		identity: identity,
		runtimes: runtimes,
		rpcSem:   make(chan struct{}, maxInFlightRPCPerClient),
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	h.register(c)

	go c.writePump()
	go c.readPump()
}

// NotifyTaskAvailable 给 watch runtimeID 的 daemon 发一个尽力而为的“新任务”唤醒。
func (h *Hub) NotifyTaskAvailable(runtimeID, taskID string) {
	h.notifyTaskAvailable(runtimeID, taskID, "")
}

// NotifyRuntimeProfilesChanged 在 create/update/disable/delete 之后，让 workspaceID
// 里连着的 daemon 拉取 runtime profiles。
func (h *Hub) NotifyRuntimeProfilesChanged(workspaceID, profileID string) {
	h.notifyRuntimeProfilesChanged(workspaceID, profileID, "")
}

// NotifyWorkspacesChanged 让每个“以 userID 鉴权”的已连接 daemon 对账它的 workspace
// 成员集合。
func (h *Hub) NotifyWorkspacesChanged(userID string) {
	h.notifyWorkspacesChanged(userID, "")
}

// NotifyPendingWork 告诉 watch runtimeID 的 daemon：有“心跳携带的请求”排队，让它们
// 立刻心跳而不是等下一个预定 tick（MUL-5444）。与 hub 的每个其它通知一样尽力而为：
// daemon 自己的心跳节奏仍是正确性路径。
func (h *Hub) NotifyPendingWork(runtimeID, kind string) {
	h.notifyPendingWork(runtimeID, kind, "")
}

func (h *Hub) notifyTaskAvailable(runtimeID, taskID, eventID string) {
	if h == nil || runtimeID == "" {
		return
	}
	data, err := taskAvailableFrame(runtimeID, taskID)
	if err != nil {
		return
	}
	delivered, deduped := h.notifyFrame(runtimeID, data, eventID)
	if delivered {
		M.WakeupDeliveredHit.Add(1)
	} else if !deduped {
		M.WakeupDeliveredMiss.Add(1)
	}
}

func (h *Hub) notifyRuntimeProfilesChanged(workspaceID, profileID, eventID string) {
	if h == nil || workspaceID == "" {
		return
	}
	data, err := runtimeProfilesChangedFrame(workspaceID, profileID)
	if err != nil {
		return
	}
	h.notifyWorkspaceFrame(workspaceID, data, eventID)
}

func (h *Hub) notifyWorkspacesChanged(userID, eventID string) {
	if h == nil || userID == "" {
		return
	}
	data, err := workspacesChangedFrame()
	if err != nil {
		return
	}
	h.notifyUserFrame(userID, data, eventID)
}

func (h *Hub) notifyPendingWork(runtimeID, kind, eventID string) {
	if h == nil || runtimeID == "" {
		return
	}
	data, err := pendingWorkFrame(runtimeID, kind)
	if err != nil {
		return
	}
	delivered, deduped := h.notifyFrame(runtimeID, data, eventID)
	if delivered {
		M.WakeupDeliveredHit.Add(1)
	} else if !deduped {
		M.WakeupDeliveredMiss.Add(1)
	}
}

// DeliverDaemonRuntime 处理“从 realtime relay 转发来的”daemon 唤醒帧：先解析出类型，
// 再按 payload 里的 runtime/workspace/user 作用域投递给本机连着的 daemon。
func (h *Hub) DeliverDaemonRuntime(scopeID string, frame []byte, eventID string) {
	if h == nil {
		return
	}
	M.WakeupReceivedTotal.Add(1)
	var msg protocol.Message
	if err := json.Unmarshal(frame, &msg); err != nil {
		slog.Debug("daemon websocket relay: invalid frame", "error", err, "scope_id", scopeID, "event_id", eventID)
		M.WakeupDeliveredMiss.Add(1)
		return
	}
	switch msg.Type {
	case protocol.EventDaemonTaskAvailable:
		var payload protocol.TaskAvailablePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil || payload.RuntimeID == "" {
			slog.Debug("daemon websocket relay: invalid task_available payload", "error", err, "scope_id", scopeID, "event_id", eventID)
			M.WakeupDeliveredMiss.Add(1)
			return
		}
		delivered, deduped := h.notifyFrame(payload.RuntimeID, frame, eventID)
		if delivered {
			M.WakeupDeliveredHit.Add(1)
		} else if !deduped {
			M.WakeupDeliveredMiss.Add(1)
		}
	case protocol.EventDaemonRuntimeProfilesChanged:
		var payload protocol.RuntimeProfilesChangedPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil || payload.WorkspaceID == "" {
			slog.Debug("daemon websocket relay: invalid runtime_profiles_changed payload", "error", err, "scope_id", scopeID, "event_id", eventID)
			M.WakeupDeliveredMiss.Add(1)
			return
		}
		delivered, deduped := h.notifyWorkspaceFrame(payload.WorkspaceID, frame, eventID)
		if delivered {
			M.WakeupDeliveredHit.Add(1)
		} else if !deduped {
			M.WakeupDeliveredMiss.Add(1)
		}
	case protocol.EventDaemonWorkspacesChanged:
		delivered, deduped := h.notifyUserFrame(scopeID, frame, eventID)
		if delivered {
			M.WakeupDeliveredHit.Add(1)
		} else if !deduped {
			M.WakeupDeliveredMiss.Add(1)
		}
	case protocol.EventDaemonPendingWork:
		var payload protocol.PendingWorkPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil || payload.RuntimeID == "" {
			slog.Debug("daemon websocket relay: invalid pending_work payload", "error", err, "scope_id", scopeID, "event_id", eventID)
			M.WakeupDeliveredMiss.Add(1)
			return
		}
		delivered, deduped := h.notifyFrame(payload.RuntimeID, frame, eventID)
		if delivered {
			M.WakeupDeliveredHit.Add(1)
		} else if !deduped {
			M.WakeupDeliveredMiss.Add(1)
		}
	default:
		M.WakeupDeliveredMiss.Add(1)
		return
	}
}

// notifyFrame 把 data 投递给挂在 runtimeID 下的所有客户端；发不进去（慢消费者）的
// 逐出。返回 (是否至少投递成功, 是否有因去重被跳过的)。
func (h *Hub) notifyFrame(runtimeID string, data []byte, eventID string) (delivered bool, deduped bool) {
	h.mu.RLock()
	clients := h.byRuntime[runtimeID]
	slow := make([]*client, 0)
	for c := range clients {
		if !c.markSeen(eventID) {
			deduped = true
			continue
		}
		select {
		case c.send <- data:
			delivered = true
		default:
			slow = append(slow, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range slow {
		h.unregister(c)
		c.conn.Close()
	}
	if len(slow) > 0 {
		M.SlowEvictionsTotal.Add(int64(len(slow)))
	}
	return delivered, deduped
}

// notifyWorkspaceFrame 是 notifyFrame 的 workspace 版。
func (h *Hub) notifyWorkspaceFrame(workspaceID string, data []byte, eventID string) (delivered bool, deduped bool) {
	h.mu.RLock()
	clients := h.byWorkspace[workspaceID]
	slow := make([]*client, 0)
	for c := range clients {
		if !c.markSeen(eventID) {
			deduped = true
			continue
		}
		select {
		case c.send <- data:
			delivered = true
		default:
			slow = append(slow, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range slow {
		h.unregister(c)
		c.conn.Close()
	}
	if len(slow) > 0 {
		M.SlowEvictionsTotal.Add(int64(len(slow)))
	}
	return delivered, deduped
}

// notifyUserFrame 是 notifyFrame 的 user 版。
func (h *Hub) notifyUserFrame(userID string, data []byte, eventID string) (delivered bool, deduped bool) {
	h.mu.RLock()
	clients := h.byUser[userID]
	slow := make([]*client, 0)
	for c := range clients {
		if !c.markSeen(eventID) {
			deduped = true
			continue
		}
		select {
		case c.send <- data:
			delivered = true
		default:
			slow = append(slow, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range slow {
		h.unregister(c)
		c.conn.Close()
	}
	if len(slow) > 0 {
		M.SlowEvictionsTotal.Add(int64(len(slow)))
	}
	return delivered, deduped
}

// taskAvailableFrame 组装“新任务可用”的协议帧。
func taskAvailableFrame(runtimeID, taskID string) ([]byte, error) {
	return json.Marshal(protocol.Message{
		Type: protocol.EventDaemonTaskAvailable,
		Payload: mustMarshalRaw(protocol.TaskAvailablePayload{
			RuntimeID: runtimeID,
			TaskID:    taskID,
		}),
	})
}

// runtimeProfilesChangedFrame 组装“runtime profile 变了”的协议帧。
func runtimeProfilesChangedFrame(workspaceID, profileID string) ([]byte, error) {
	return json.Marshal(protocol.Message{
		Type: protocol.EventDaemonRuntimeProfilesChanged,
		Payload: mustMarshalRaw(protocol.RuntimeProfilesChangedPayload{
			WorkspaceID:      workspaceID,
			RuntimeProfileID: profileID,
		}),
	})
}

// workspacesChangedFrame 组装“workspaces 变了”的协议帧。
func workspacesChangedFrame() ([]byte, error) {
	return json.Marshal(protocol.Message{
		Type:    protocol.EventDaemonWorkspacesChanged,
		Payload: mustMarshalRaw(protocol.WorkspacesChangedPayload{}),
	})
}

// pendingWorkFrame 组装“有待办工作”的协议帧。
func pendingWorkFrame(runtimeID, kind string) ([]byte, error) {
	return json.Marshal(protocol.Message{
		Type: protocol.EventDaemonPendingWork,
		Payload: mustMarshalRaw(protocol.PendingWorkPayload{
			RuntimeID: runtimeID,
			Kind:      kind,
		}),
	})
}

// mustMarshalRaw 序列化 v；失败返回 nil（对已知可序列化载荷永不应发生）。
func mustMarshalRaw(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// RuntimeConnectionCount 返回 watch 某 runtime 的连接数。
func (h *Hub) RuntimeConnectionCount(runtimeID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byRuntime[runtimeID])
}

// WorkspaceConnectionCount 返回 watch 某 workspace 的连接数。
func (h *Hub) WorkspaceConnectionCount(workspaceID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byWorkspace[workspaceID])
}

// UserConnectionCount 返回以某 user 鉴权的连接数。
func (h *Hub) UserConnectionCount(userID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byUser[userID])
}

// register 把连接按 runtime / workspace / user 三个索引登记，并更新计数与日志。
func (h *Hub) register(c *client) {
	h.mu.Lock()
	h.clients[c] = true
	for runtimeID := range c.runtimes {
		conns := h.byRuntime[runtimeID]
		if conns == nil {
			conns = make(map[*client]bool)
			h.byRuntime[runtimeID] = conns
		}
		conns[c] = true
	}
	workspaceIDs := c.identity.AuthorizedWorkspaceIDs()
	for _, workspaceID := range workspaceIDs {
		conns := h.byWorkspace[workspaceID]
		if conns == nil {
			conns = make(map[*client]bool)
			h.byWorkspace[workspaceID] = conns
		}
		conns[c] = true
	}
	if userID := c.identity.UserID; userID != "" {
		conns := h.byUser[userID]
		if conns == nil {
			conns = make(map[*client]bool)
			h.byUser[userID] = conns
		}
		conns[c] = true
	}
	total := len(h.clients)
	h.mu.Unlock()

	M.ConnectsTotal.Add(1)
	M.ActiveConnections.Add(1)
	slog.Info("daemon websocket connected",
		"daemon_id", c.identity.DaemonID,
		"user_id", c.identity.UserID,
		"workspace_id", c.identity.PrimaryWorkspaceID(),
		"workspace_ids", workspaceIDs,
		"runtimes", len(c.runtimes),
		"client_version", c.identity.ClientVersion,
		"total_clients", total,
	)
}

// unregister 把连接从全部索引摘除、关掉它的 send channel（幂等——不在册则直接返回）。
func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	if !h.clients[c] {
		h.mu.Unlock()
		return
	}
	delete(h.clients, c)
	for runtimeID := range c.runtimes {
		if conns := h.byRuntime[runtimeID]; conns != nil {
			delete(conns, c)
			if len(conns) == 0 {
				delete(h.byRuntime, runtimeID)
			}
		}
	}
	workspaceIDs := c.identity.AuthorizedWorkspaceIDs()
	for _, workspaceID := range workspaceIDs {
		if conns := h.byWorkspace[workspaceID]; conns != nil {
			delete(conns, c)
			if len(conns) == 0 {
				delete(h.byWorkspace, workspaceID)
			}
		}
	}
	if userID := c.identity.UserID; userID != "" {
		if conns := h.byUser[userID]; conns != nil {
			delete(conns, c)
			if len(conns) == 0 {
				delete(h.byUser, userID)
			}
		}
	}
	c.sendMu.Lock()
	c.sendClosed = true
	close(c.send)
	c.sendMu.Unlock()
	total := len(h.clients)
	h.mu.Unlock()

	M.DisconnectsTotal.Add(1)
	M.ActiveConnections.Add(-1)
	slog.Info("daemon websocket disconnected",
		"daemon_id", c.identity.DaemonID,
		"user_id", c.identity.UserID,
		"workspace_id", c.identity.PrimaryWorkspaceID(),
		"workspace_ids", workspaceIDs,
		"runtimes", len(c.runtimes),
		"total_clients", total,
	)
}

// readPump 循环读帧并分派；连接断开时按序收尾（先取消 ctx 让 RPC 停，再注销）。
func (c *client) readPump() {
	defer func() {
		// 先取消，让异步 RPC handler 在我们关闭 send channel 之前停下；然后注销（它
		// 会标记 send closed）。
		c.cancel()
		c.hub.unregister(c)
		c.conn.Close()
	}()

	// 读上限按 daemon:rpc_request 帧（携带机器完整 runtime_id 集合，MUL-4257）定，
	// 远高于细小的 heartbeat/wakeup 帧。
	c.conn.SetReadLimit(64 * 1024)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Debug("daemon websocket read error", "error", err, "daemon_id", c.identity.DaemonID)
			}
			return
		}
		c.handleFrame(raw)
	}
}

// handleFrame 解析一条入站帧并按类型分派；未知应用消息为向前兼容而被有意忽略。
func (c *client) handleFrame(raw []byte) {
	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		slog.Debug("daemon websocket invalid frame", "error", err, "daemon_id", c.identity.DaemonID)
		if rec := c.hub.messageKindRecorder(); rec != nil {
			rec.RecordDaemonWSMessageReceived("invalid")
		}
		return
	}
	kind := strings.TrimPrefix(msg.Type, "daemon:")
	if kind == "" {
		kind = "unknown"
	}
	if rec := c.hub.messageKindRecorder(); rec != nil {
		rec.RecordDaemonWSMessageReceived(kind)
	}
	switch msg.Type {
	case protocol.EventDaemonHeartbeat:
		c.handleHeartbeatFrame(msg.Payload)
	case protocol.EventDaemonRPCRequest:
		c.handleRPCFrame(msg.Payload)
	default:
		// 未知应用消息为向前兼容（将来的 daemon → server 消息类型）而被有意忽略。
	}
}

// handleRPCFrame 处理一条通用 daemon:rpc_request（MUL-4257）：在独立 goroutine 里跑
// 注册的 RPC handler（让一个吃 DB 的 claim 不会卡住读泵或下一拍心跳），并写回一条带回
// 显式请求 id 的 daemon:rpc_response。handler 缺失或在途槽满时给非 2xx 响应，让 daemon
// 回退 HTTP。
func (c *client) handleRPCFrame(raw json.RawMessage) {
	var req protocol.RPCRequestPayload
	if err := json.Unmarshal(raw, &req); err != nil {
		slog.Debug("daemon websocket rpc invalid payload", "error", err, "daemon_id", c.identity.DaemonID)
		return
	}
	if req.RequestID == "" {
		slog.Debug("daemon websocket rpc missing request_id", "daemon_id", c.identity.DaemonID)
		return
	}
	handler := c.hub.rpcHandler()
	if handler == nil {
		c.sendRPCResponse(req.RequestID, http.StatusServiceUnavailable, nil, "rpc handler unavailable")
		return
	}
	// 限制并发 handler；若已饱和，让 daemon 回退而不是在一条 socket 上排无界的工作。
	select {
	case c.rpcSem <- struct{}{}:
	default:
		c.sendRPCResponse(req.RequestID, http.StatusTooManyRequests, nil, "too many in-flight rpc requests")
		return
	}
	go func() {
		defer func() { <-c.rpcSem }()
		// 用调用方申请的预算（在连接 ctx 之外）约束服务端执行：慢 RPC 会被取消——其
		// 工作一并回滚——而不是在 daemon 已超时回退 HTTP 之后才提交（MUL-4257）。
		// daemon 会在此预算之外再等一段宽限期，因此“截止前确实提交”的 claim 仍能及时
		// 报回来。
		hctx := c.ctx
		if req.TimeoutMs > 0 {
			var cancel context.CancelFunc
			hctx, cancel = context.WithTimeout(c.ctx, time.Duration(req.TimeoutMs)*time.Millisecond)
			defer cancel()
		}
		completion := &rpcCompletion{}
		hctx = context.WithValue(hctx, rpcCompletionKey{}, completion)
		defer completion.run()
		status, body, err := handler(hctx, c.identity, req.Method, req.Body)
		if err != nil {
			if status < 400 {
				status = http.StatusInternalServerError
			}
			c.sendRPCResponse(req.RequestID, status, nil, err.Error())
			return
		}
		c.sendRPCResponse(req.RequestID, status, body, "")
	}()
}

// sendRPCResponse 组一条 daemon:rpc_response 帧并尝试投递；失败则丢弃（daemon 的按
// 请求超时会触发并回退 HTTP）。
func (c *client) sendRPCResponse(requestID string, status int, body json.RawMessage, errMsg string) {
	frame, err := json.Marshal(protocol.Message{
		Type: protocol.EventDaemonRPCResponse,
		Payload: mustMarshalRaw(protocol.RPCResponsePayload{
			RequestID: requestID,
			Status:    status,
			Body:      body,
			Error:     errMsg,
		}),
	})
	if err != nil {
		slog.Debug("daemon websocket rpc response marshal failed", "error", err)
		return
	}
	if !c.trySend(frame) {
		// 发送缓冲满或连接关闭中——丢下响应；daemon 的按请求超时触发、回退 HTTP。
		slog.Debug("daemon websocket rpc response dropped",
			"daemon_id", c.identity.DaemonID, "request_id", requestID)
	}
}

// handleHeartbeatFrame 处理 daemon 发来的入站 daemon:heartbeat：调用 hub 的 handler，
// 并写回 daemon:heartbeat_ack。
func (c *client) handleHeartbeatFrame(raw json.RawMessage) {
	handler := c.hub.heartbeatHandler()
	if handler == nil {
		// 服务器没有接心跳 handler——daemon 等 ack 会超时并回退 HTTP 心跳。
		return
	}

	var payload protocol.DaemonHeartbeatRequestPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Debug("daemon websocket heartbeat invalid payload", "error", err, "daemon_id", c.identity.DaemonID)
		return
	}
	if payload.RuntimeID == "" {
		slog.Debug("daemon websocket heartbeat missing runtime_id", "daemon_id", c.identity.DaemonID)
		return
	}
	if _, ok := c.runtimes[payload.RuntimeID]; !ok {
		// 连接鉴权时固定了 runtime 集合；拒绝任何“客户端没注册过”的 runtime 心跳。
		slog.Warn("daemon websocket heartbeat for unauthorized runtime",
			"daemon_id", c.identity.DaemonID,
			"runtime_id", payload.RuntimeID)
		return
	}

	// 刻意不用 WithTimeout 包这个 ctx。handler 会触达
	// LocalSkill{List,Import}Store.PopPending，其 Redis Lua claim 脚本有副作用
	// （ZREM + SET-running），客户端在中途取消无法安全回滚——同一不变量也是 HTTP 心跳
	// 不给 PopPending 加按调用超时的原因。自然边界是读泵的存活期（daemon 走掉连接就会
	// 关）外加 Redis 自身的服务端上限。
	ack, err := handler(context.Background(), c.identity, payload.RuntimeID, payload.SupportsBatchImport)
	if err != nil {
		slog.Warn("daemon websocket heartbeat handler failed",
			"error", err,
			"daemon_id", c.identity.DaemonID,
			"runtime_id", payload.RuntimeID)
		return
	}
	if ack == nil {
		return
	}
	frame, err := json.Marshal(protocol.Message{
		Type:    protocol.EventDaemonHeartbeatAck,
		Payload: mustMarshalRaw(ack),
	})
	if err != nil {
		slog.Debug("daemon websocket heartbeat ack marshal failed", "error", err)
		return
	}
	if !c.trySend(frame) {
		// 发送缓冲满或连接关闭中——丢下；HTTP 心跳恢复。
		slog.Debug("daemon websocket heartbeat ack dropped",
			"daemon_id", c.identity.DaemonID,
			"runtime_id", payload.RuntimeID)
	}
}

// writePump 单点串行写：负责投递 send 队列里的帧，并周期发 ping 保活。
func (c *client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				slog.Debug("daemon websocket write error", "error", err, "daemon_id", c.identity.DaemonID)
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
