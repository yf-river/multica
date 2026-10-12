// hub.go 是 realtime 包的单机版核心：Hub 把每个 WebSocket 连接按「作用域
// (scope)」收进不同的房间，负责连接的注册/注销、订阅/退订、把消息扇给房间里
// 的所有连接、剔除写入太慢的「慢客户端」；文件后半还包含 WebSocket 握手鉴权
// （Cookie 或首帧令牌）与每条连接各自的读泵/写泵（readPump/writePump）。
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/auth"
)

// MembershipChecker 校验「某用户是否属于某 workspace」。WS 连上后必须先过这一关，
// 防止把别的空间的事件推给无权接收的人。
type MembershipChecker interface {
	IsMember(ctx context.Context, userID, workspaceID string) bool
}

// SlugResolver 把 workspace 的短名(slug)翻译成它的 UUID。连接时如果只给了
// workspace_slug 而没给 workspace_id，就用它查。
type SlugResolver func(ctx context.Context, slug string) (workspaceID string, err error)

// PATResolver 把一个 Personal Access Token（个人访问令牌，形如 mul_xxx）还原成
// 用户 ID。返回的 ok 表示令牌是否有效。
type PATResolver interface {
	ResolveToken(ctx context.Context, token string) (userID string, ok bool)
}

// ScopeAuthorizer 决定某条连接（用 userID+workspaceID 标识）是否有权订阅某个
// 作用域。实现方通常要去数据库里查一下底层资源（task / chat 会话），确认它确实
// 属于 workspaceID。实现方应缓存查询成功的结论，避免高频热路径每次都打数据库。
type ScopeAuthorizer interface {
	AuthorizeScope(ctx context.Context, userID, workspaceID, scopeType, scopeID string) (bool, error)
}

// 下面两组「全局配置」用 atomic.Value 保存，这样任何时刻读到的都是一份完整的切片，
// 不会出现一边被改写一边读到半个数组的问题。进程启动时先从环境变量读一份默认值，
// 服务器启动后若显式配置过，会用 Set* 覆盖成同一份配置。
var allowedWSOrigins atomic.Value // holds []string
var trustedProxies atomic.Value   // holds []netip.Prefix

func init() {
	allowedWSOrigins.Store(loadAllowedOrigins())
	trustedProxies.Store(loadTrustedProxies())
}

// loadAllowedOrigins 从环境变量里读「允许跨域连 WebSocket 的 Origin 白名单」，
// 依次尝试 ALLOWED_ORIGINS、CORS_ALLOWED_ORIGINS、FRONTEND_ORIGIN 三个变量（按
// 出现的先后，取第一个非空的）。都没有时就退回只允许本地开发地址
// http://localhost:3000。列表用逗号分隔，逗号周围允许留空格。
func loadAllowedOrigins() []string {
	raw := strings.TrimSpace(os.Getenv("ALLOWED_ORIGINS"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS"))
	}
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
	}
	if raw == "" {
		return []string{
			"http://localhost:3000",
		}
	}

	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin != "" {
			origins = append(origins, origin)
		}
	}
	return origins
}

// loadTrustedProxies 读取整个 server 共用的 MULTICA_TRUSTED_PROXIES 环境变量
// （其它地方的使用见 cmd/server/router.go 与 handler.Config.TrustedProxies），
// 把它按逗号拆成一串 CIDR 网段。条目非法就丢掉并打一条 warn，而不是让整个进程
// 崩溃。空输入返回 nil，含义是「不信任任何代理」——此时 X-Forwarded-Host 一律不采信。
// 路由器在启动时用 SetTrustedProxies 覆盖本函数的结果，保证 HTTP 与 WS 共用一份配置。
func loadTrustedProxies() []netip.Prefix {
	raw := strings.TrimSpace(os.Getenv("MULTICA_TRUSTED_PROXIES"))
	if raw == "" {
		return nil
	}
	var prefixes []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			slog.Warn("ws: ignoring invalid trusted proxy CIDR", "value", s, "error", err)
			continue
		}
		prefixes = append(prefixes, p)
	}
	return prefixes
}

// SetAllowedOrigins 覆盖 WebSocket 的 Origin 白名单（整块原子替换，不是逐条改）。
func SetAllowedOrigins(origins []string) {
	allowedWSOrigins.Store(origins)
}

// SetTrustedProxies 覆盖受信任代理的 CIDR 网段列表。服务器启动时会把共用的
// MULTICA_TRUSTED_PROXIES 值填进来，保证 WS 与 HTTP 判断一致。
func SetTrustedProxies(proxies []netip.Prefix) {
	trustedProxies.Store(proxies)
}

// isTrustedProxy 判断请求的远端地址是否落在某个受信任代理网段内。只有来源可信时，
// 后续才允许它提供的 X-Forwarded-* 头参与鉴权判断。
func isTrustedProxy(remoteAddr string) bool {
	proxies := trustedProxies.Load().([]netip.Prefix)
	if len(proxies) == 0 {
		return false
	}
	addr, err := netip.ParseAddr(remoteHost(remoteAddr))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range proxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// remoteHost 从 http.Request.RemoteAddr 里剥出主机/IP。RemoteAddr 通常是
// "host:port"，带中括号的 IPv6（"[::1]:443"）交给 net.SplitHostPort 处理；万一
// 没有端口号，就退回用原值去中括号后返回。
func remoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return strings.Trim(remoteAddr, "[]")
}

// firstForwardedHost 返回 X-Forwarded-Host 里的第一个主机名。代理链是从左到右
// 追加值的，所以第一项是客户端真正访问的公网主机名——也就是要拿来和 Origin
// 比对的那个。
func firstForwardedHost(h string) string {
	if i := strings.IndexByte(h, ','); i >= 0 {
		h = h[:i]
	}
	return strings.TrimSpace(h)
}

// checkOrigin 是 WebSocket 升级前的「跨域来源」安全检查：请求带 Origin 头时，只放行
// 同源或白名单内来源；不带 Origin（非浏览器客户端）直接放行。
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	// 同源判定：原生客户端（移动端、CLI）没有真正的页面域名，其 WebSocket 库会把
	// Origin 填成连接目标——恰好等于服务器自己的 Host。它们用 bearer 令牌而不是
	// 自动携带的 Cookie 鉴权，所以下面白名单要防的 CSRF（浏览器代发）攻击对它们
	// 不成立。这与 gorilla/websocket 默认 CheckOrigin 行为一致；白名单是在此之外
	// 额外支持跨域浏览器客户端（web/desktop）。
	if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, r.Host) {
		return true
	}
	// 反代场景：服务器藏在代理后面时，Host 头是内网地址，X-Forwarded-Host 才带着
	// 客户端看到的公网主机名，所以它和 Origin 一致也算同源。安全前提：只有当请求
	// 确实来自受信任代理时才采信 X-Forwarded-Host，否则任何人都能伪造它绕过白名单。
	if fwdHost := firstForwardedHost(r.Header.Get("X-Forwarded-Host")); fwdHost != "" && isTrustedProxy(r.RemoteAddr) {
		if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, fwdHost) {
			return true
		}
	}
	origins := allowedWSOrigins.Load().([]string)
	for _, allowed := range origins {
		if origin == allowed {
			return true
		}
	}
	slog.Warn("ws: rejected origin", "origin", origin, "remote_addr", r.RemoteAddr)
	return false
}

// WebSocket 心跳与限流的计时参数。gorilla/websocket 的惯例：服务端定期发 ping
// （pingPeriod），客户端要在 pongWait 内回 pong，否则判死。writeWait 是每次写操作
// 允许阻塞的最长时间，超过就放弃这条连接，避免某个读不到数据的死客户端一直占着
// 写线程。
const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10

	// inboundReadLimit 限制单条入站消息的大小。客户端合法发的每条帧都很小——最大
	// 的令牌鉴权帧也不到 1 KiB——但 gorilla 会把一整条消息先全读进内存才交给应用，
	// 而且分片消息会在穿插 pong 的过程中一直续着读超时。不设上限的话，一条连接就能
	// 把这个缓冲区无限撑大，把进程 OOM。取值与 daemon 的 hub 一致，保证两套
	// WebSocket 面对「一条消息能多大」这个问题给出相同答案。
	inboundReadLimit = 64 * 1024
)

var upgrader = websocket.Upgrader{
	CheckOrigin: checkOrigin,
}

// scopeKey 是查「订阅某房间的一批连接」时用的复合键。Type 是作用域种类
// (workspace/user/task/chat…)，ID 是具体对象的 id。
type scopeKey struct {
	Type string
	ID   string
}

func sk(t, id string) scopeKey { return scopeKey{Type: t, ID: id} }

// Client 代表一条 WebSocket 连接，携带身份信息以及它当前订阅的所有作用域。
type Client struct {
	hub         *Hub
	conn        *websocket.Conn
	send        chan []byte
	userID      string
	workspaceID string

	// subscriptions 记录该连接当前在哪些房间，受 hub.mu 保护。断线清理房间时要靠它
	// 知道该把连接从哪些房间里摘出去。
	subscriptions map[scopeKey]bool

	// 去重缓冲（seenIDs + seenList）：双写广播（先走本机快路径，再从 Redis 重放一遍）
	// 以及其它未来的投递方会靠 eventID 去重，保证同一条消息即使两条路都到也不会推两遍。
	// 因为 eventID 是 ULID、只需要记住最近几条，所以用有界 LRU 语义就够了。
	dedupMu  sync.Mutex
	seenIDs  map[string]struct{}
	seenList []string
}

// dedupCapacity 是每个连接最多记住的最近已投递 eventID 数量，超出就从最老的开始丢。
const dedupCapacity = 128

// markSeen 把 eventID 记成「已投递给这条连接」。返回 true 表示第一次见到这个 id
// （调用方应该投递），false 表示重复（调用方应丢弃）。eventID 为空时永远视为新事件。
func (c *Client) markSeen(eventID string) bool {
	if eventID == "" {
		return true
	}
	c.dedupMu.Lock()
	defer c.dedupMu.Unlock()
	if c.seenIDs == nil {
		c.seenIDs = make(map[string]struct{}, dedupCapacity)
	}
	if _, ok := c.seenIDs[eventID]; ok {
		return false
	}
	c.seenIDs[eventID] = struct{}{}
	c.seenList = append(c.seenList, eventID)
	if len(c.seenList) > dedupCapacity {
		drop := c.seenList[0]
		c.seenList = c.seenList[1:]
		delete(c.seenIDs, drop)
	}
	return true
}

// SubscriptionCallback 在某作用域的本地订阅人数跨过 0↔1 边界时触发。Redis relay 靠它
// 按需启动/停止某个作用域对应的 XREADGROUP 消费循环：没人订阅就停下别占资源，
// 有人来了再启动。
type SubscriptionCallback func(scopeType, scopeID string)

// Hub 把 WebSocket 连接按作用域组织成房间，提供注册/注销、订阅/退订和广播能力。
//
// 并发模型：所有房间/连接集合的读写都集中在少量方法里用 mu 保护；broadcast、
// register、unregister 三个 channel 由 Run 里的单一事件循环消费，实现「改状态走锁、
// 大扇出走锁+非阻塞入队」的折中。
type Hub struct {
	rooms      map[scopeKey]map[*Client]bool
	clients    map[*Client]bool // 所有已连接的客户端（全局广播与快照都要用）
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex

	authorizer ScopeAuthorizer

	// 订阅生命周期钩子。两者都可以是 nil。
	onFirstSubscriber SubscriptionCallback
	onLastSubscriber  SubscriptionCallback
}

// NewHub 创建一个新的 Hub 实例。
func NewHub() *Hub {
	return &Hub{
		rooms:      make(map[scopeKey]map[*Client]bool),
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// SetAuthorizer 把 ScopeAuthorizer 装进 hub。在 Run 之前调用是安全的。
func (h *Hub) SetAuthorizer(a ScopeAuthorizer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.authorizer = a
}

// SetSubscriptionCallbacks 注册两个回调：本节点某个作用域从 0→1 个订阅者时触发
// onFirst，从 1→0 时触发 onLast。Redis relay 用它们来按作用域启动/停止消费循环。
func (h *Hub) SetSubscriptionCallbacks(onFirst, onLast SubscriptionCallback) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onFirstSubscriber = onFirst
	h.onLastSubscriber = onLast
}

// Run 启动 hub 的事件循环，处理注册、注销与全局广播三类事件。
// 这是一个永不返回的死循环，通常用独立 goroutine 跑。
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			total := len(h.clients)
			h.mu.Unlock()
			M.ConnectsTotal.Add(1)
			M.ActiveConnections.Add(1)
			// 连接一进来就自动订阅它所属的 workspace 与 user 两个作用域，之后这两类
			// 事件无需显式订阅也能收到。
			h.subscribe(client, ScopeWorkspace, client.workspaceID)
			if client.userID != "" {
				h.subscribe(client, ScopeUser, client.userID)
			}
			slog.Info("ws client connected", "workspace_id", client.workspaceID, "user_id", client.userID, "total_clients", total)

		case client := <-h.unregister:
			h.removeClient(client)

		case message := <-h.broadcast:
			h.fanoutAll(message, "")
		}
	}
}

// removeClient 把一条连接从所有房间和全局集合里移除（断线或慢客户端被踢共用此路）。
// 房间被清空时顺手触发 onLastSubscriber 回调并计数。
func (h *Hub) removeClient(client *Client) {
	h.mu.Lock()
	if !h.clients[client] {
		h.mu.Unlock()
		return
	}
	delete(h.clients, client)
	subs := client.subscriptions
	client.subscriptions = nil
	emptied := make([]scopeKey, 0, len(subs))
	for key := range subs {
		if room, ok := h.rooms[key]; ok {
			delete(room, client)
			if len(room) == 0 {
				delete(h.rooms, key)
				emptied = append(emptied, key)
			}
		}
	}
	close(client.send)
	cb := h.onLastSubscriber
	total := len(h.clients)
	h.mu.Unlock()

	M.DisconnectsTotal.Add(1)
	M.ActiveConnections.Add(-1)
	if cb != nil {
		for _, key := range emptied {
			cb(key.Type, key.ID)
		}
	}
	for _, key := range emptied {
		M.DecRoom(key.Type)
	}
	slog.Info("ws client disconnected", "workspace_id", client.workspaceID, "user_id", client.userID, "total_clients", total)
}

// subscribe 把 client 加入 (scopeType, scopeID) 房间。如果房间因此从空变成非空，
// 触发 onFirstSubscriber 回调。返回 true 表示这次是真正新增订阅（此前没订阅过）。
func (h *Hub) subscribe(client *Client, scopeType, scopeID string) bool {
	if scopeType == "" || scopeID == "" {
		return false
	}
	key := sk(scopeType, scopeID)

	h.mu.Lock()
	if !h.clients[client] {
		h.mu.Unlock()
		return false
	}
	if client.subscriptions == nil {
		client.subscriptions = map[scopeKey]bool{}
	}
	if client.subscriptions[key] {
		h.mu.Unlock()
		return false
	}
	client.subscriptions[key] = true
	room, ok := h.rooms[key]
	first := false
	if !ok {
		room = make(map[*Client]bool)
		h.rooms[key] = room
		first = true
	}
	room[client] = true
	cb := h.onFirstSubscriber
	h.mu.Unlock()

	M.SubscribesTotal(scopeType).Add(1)
	if first {
		M.IncRoom(scopeType)
		if cb != nil {
			cb(scopeType, scopeID)
		}
	}
	return true
}

// unsubscribe 把 client 从某个作用域房间移除。如果房间因此空了，触发
// onLastSubscriber 回调。返回 true 表示确实退掉了一个订阅。
func (h *Hub) unsubscribe(client *Client, scopeType, scopeID string) bool {
	if scopeType == "" || scopeID == "" {
		return false
	}
	key := sk(scopeType, scopeID)

	h.mu.Lock()
	if !h.clients[client] {
		h.mu.Unlock()
		return false
	}
	if client.subscriptions == nil || !client.subscriptions[key] {
		h.mu.Unlock()
		return false
	}
	delete(client.subscriptions, key)
	emptied := false
	if room, ok := h.rooms[key]; ok {
		delete(room, client)
		if len(room) == 0 {
			delete(h.rooms, key)
			emptied = true
		}
	}
	cb := h.onLastSubscriber
	h.mu.Unlock()

	M.UnsubscribesTotal(scopeType).Add(1)
	if emptied {
		M.DecRoom(scopeType)
		if cb != nil {
			cb(scopeType, scopeID)
		}
	}
	return true
}

// HasLocalSubscribers 报告本机是否至少有一个客户端订阅了 (scopeType, scopeID)。
// Redis relay 靠它决定要不要继续跑某个作用域的消费循环。
func (h *Hub) HasLocalSubscribers(scopeType, scopeID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.rooms[sk(scopeType, scopeID)]
	return ok
}

// LocalScopes 返回本节点当前活跃的所有作用域。只是快照，调用方不能假设它在多次
// 调用间保持稳定（其它 goroutine 随时可能增删房间）。
func (h *Hub) LocalScopes() []scopeKey {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]scopeKey, 0, len(h.rooms))
	for k := range h.rooms {
		out = append(out, k)
	}
	return out
}

// BroadcastToScope 把一条消息发给 (scopeType, scopeID) 房间里的每个客户端。
// 对于发不进去的慢客户端，会在持写锁之外统一走 evictSlow 剔除。
func (h *Hub) BroadcastToScope(scopeType, scopeID string, message []byte) {
	h.BroadcastToScopeDedup(scopeType, scopeID, message, "")
}

// BroadcastToScopeDedup 与 BroadcastToScope 相同，但会跳过那些已经见过 eventID 的
// 客户端（Redis relay 用它去掉 DualWriteBroadcaster 本地快路径造成的重复投递）。
func (h *Hub) BroadcastToScopeDedup(scopeType, scopeID string, message []byte, eventID string) {
	if scopeType == "" || scopeID == "" {
		return
	}
	key := sk(scopeType, scopeID)

	h.mu.RLock()
	clients := h.rooms[key]
	var slow []*Client
	var sent int64
	for client := range clients {
		if !client.markSeen(eventID) {
			continue
		}
		select {
		case client.send <- message:
			sent++
		default:
			slow = append(slow, client)
		}
	}
	h.mu.RUnlock()

	if sent > 0 {
		M.MessagesSentTotal.Add(sent)
	}
	if len(slow) > 0 {
		h.evictSlow(slow)
	}
}

// fanoutAll 把 message 发给每个已连接客户端。excludeWorkspace 非空时，跳过那些
// workspaceID 与它相同的连接（这是从 SendToUser 继承来的 member:added 去重语义）。
// eventID 是去重键（为空表示不去重）。
func (h *Hub) fanoutAll(message []byte, excludeWorkspace string) {
	h.fanoutAllDedup(message, excludeWorkspace, "")
}

func (h *Hub) fanoutAllDedup(message []byte, excludeWorkspace, eventID string) {
	h.mu.RLock()
	var slow []*Client
	var sent int64
	for client := range h.clients {
		if excludeWorkspace != "" && client.workspaceID == excludeWorkspace {
			continue
		}
		if !client.markSeen(eventID) {
			continue
		}
		select {
		case client.send <- message:
			sent++
		default:
			slow = append(slow, client)
		}
	}
	h.mu.RUnlock()

	if sent > 0 {
		M.MessagesSentTotal.Add(sent)
	}
	if len(slow) > 0 {
		h.evictSlow(slow)
	}
}

// BroadcastToWorkspace 是 BroadcastToScope(ScopeWorkspace, workspaceID, message)
// 的向后兼容简写。
func (h *Hub) BroadcastToWorkspace(workspaceID string, message []byte) {
	h.BroadcastToScope(ScopeWorkspace, workspaceID, message)
}

// SendToUser 把一条消息发给属于 userID 的每条连接，并跳过 workspaceID 命中
// excludeWorkspace 的连接。
func (h *Hub) SendToUser(userID string, message []byte, excludeWorkspace ...string) {
	exclude := ""
	if len(excludeWorkspace) > 0 {
		exclude = excludeWorkspace[0]
	}
	h.fanoutUser(userID, message, exclude, "")
}

// Broadcast 把消息发给所有已连接客户端（用于没有 workspace 概念的 daemon 事件）。
func (h *Hub) Broadcast(message []byte) {
	h.broadcast <- message
}

// fanoutUser 把消息发给 user 作用域房间里的所有客户端，可排除指定 workspace 的
// 连接，并可基于 eventID 去重。
func (h *Hub) fanoutUser(userID string, message []byte, excludeWorkspace, eventID string) {
	key := sk(ScopeUser, userID)
	h.mu.RLock()
	clients := h.rooms[key]
	var slow []*Client
	var sent int64
	for client := range clients {
		if excludeWorkspace != "" && client.workspaceID == excludeWorkspace {
			continue
		}
		if !client.markSeen(eventID) {
			continue
		}
		select {
		case client.send <- message:
			sent++
		default:
			slow = append(slow, client)
		}
	}
	h.mu.RUnlock()
	if sent > 0 {
		M.MessagesSentTotal.Add(sent)
	}
	if len(slow) > 0 {
		h.evictSlow(slow)
	}
}

// evictSlow 处理发送队列已满的「慢客户端」：把它们的 send channel 关掉、递减计数，
// 并顺带对被抽空（清零）的房间触发 onLastSubscriber 回调。行为与改造前一致——
// 慢即断线，宁可丢掉一个人也不让一条消息把整个广播卡死。
func (h *Hub) evictSlow(slow []*Client) {
	M.MessagesDroppedTotal.Add(int64(len(slow)))
	M.SlowEvictionsTotal.Add(int64(len(slow)))

	h.mu.Lock()
	evicted := 0
	type emptied struct {
		Type, ID string
	}
	var drainedRooms []emptied
	for _, c := range slow {
		if !h.clients[c] {
			continue
		}
		delete(h.clients, c)
		for key := range c.subscriptions {
			if room, ok := h.rooms[key]; ok {
				delete(room, c)
				if len(room) == 0 {
					delete(h.rooms, key)
					drainedRooms = append(drainedRooms, emptied{key.Type, key.ID})
				}
			}
		}
		c.subscriptions = nil
		close(c.send)
		evicted++
	}
	cb := h.onLastSubscriber
	h.mu.Unlock()

	if evicted > 0 {
		M.ActiveConnections.Add(int64(-evicted))
		M.DisconnectsTotal.Add(int64(evicted))
	}
	for _, r := range drainedRooms {
		M.DecRoom(r.Type)
	}
	if cb != nil {
		for _, r := range drainedRooms {
			cb(r.Type, r.ID)
		}
	}
}

// Snapshot 返回一份便于转成 JSON 的 hub 状态概要（连接数与各类型房间数），
// 用于健康检查或调试接口。
func (h *Hub) Snapshot() map[string]any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	rooms := map[string]int{}
	for key := range h.rooms {
		rooms[key.Type]++
	}
	return map[string]any{
		"connections": len(h.clients),
		"rooms":       rooms,
	}
}

// authenticateToken 校验一段 JWT 或 PAT 令牌，返回其中的用户 ID。
//
// 返回两个值：非空的 uid 表示鉴权通过；否则返回空 uid 和一个已经格式化好的 JSON
// 错误体（供调用方直接写回给客户端）。
func authenticateToken(tokenStr string, pr PATResolver, ctx context.Context) (string, string) {
	// PAT 形如 mul_xxx：本地查库解析，不依赖 JWT 密钥。
	if strings.HasPrefix(tokenStr, "mul_") {
		if pr == nil {
			return "", `{"error":"invalid token"}`
		}
		uid, ok := pr.ResolveToken(ctx, tokenStr)
		if !ok {
			return "", `{"error":"invalid token"}`
		}
		if auth.IsTemporarilyDisabledUserID(uid) {
			return "", `{"error":"account disabled"}`
		}
		return uid, ""
	}

	// 其余按 JWT 解析：必须用 HMAC 签名（防止 alg=none 之类降级攻击），密钥来自服务端配置。
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return auth.JWTSecret(), nil
	})
	if err != nil || !token.Valid {
		return "", `{"error":"invalid token"}`
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", `{"error":"invalid claims"}`
	}

	// sub（subject）声明即用户 ID，必须有值。
	uid, ok := claims["sub"].(string)
	if !ok || strings.TrimSpace(uid) == "" {
		return "", `{"error":"invalid claims"}`
	}
	// email 缺失时 IsTemporarilyDisabledUser 只按 uid 判断临时封禁。
	email, _ := claims["email"].(string)
	if auth.IsTemporarilyDisabledUser(uid, email) {
		return "", `{"error":"account disabled"}`
	}
	return uid, ""
}

// firstMessageAuth 读取 WebSocket 的第一条消息，期望它是鉴权载荷。
//
// 返回值约定：token 非空表示鉴权通过；errMsg 非空表示出错，调用方应把它写回给
// 客户端后关闭连接；closed=true 表示连接已经被拆除（比如超限关闭），调用方必须
// 直接返回、不要再写任何东西。
func firstMessageAuth(conn *websocket.Conn) (token, errMsg string, closed bool) {
	// 给首条消息 10 秒超时：迟迟不发鉴权帧的客户端直接判超时，避免空连接占用资源。
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	_, raw, err := conn.ReadMessage()
	if err != nil {
		if errors.Is(err, websocket.ErrReadLimit) {
			// gorilla 已经回过了 CloseMessageTooBig(1009)，这时再回一帧 auth_error 属于
			// 在 close 帧之后继续发数据。单独计数，让这种「越界」不混进普通连接抖动里。
			M.InboundTooLargeTotal.Add(1)
			slog.Warn("ws: pre-auth frame exceeded read limit", "limit_bytes", inboundReadLimit)
			conn.Close()
			return "", "", true
		}
		return "", `{"error":"auth timeout or read error"}`, false
	}

	var msg struct {
		Type    string `json:"type"`
		Payload struct {
			Token string `json:"token"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Type != "auth" || msg.Payload.Token == "" {
		return "", `{"error":"expected auth message as first frame"}`, false
	}

	return msg.Payload.Token, "", false
}

// wsMessageWriter 是 writePump 依赖的最小写接口，方便测试注入替身。
type wsMessageWriter interface {
	WriteMessage(messageType int, data []byte) error
}

// writeWSAuthFrame 尝试向连接写一帧文本消息。写失败就记一条 warn 并返回 false。
func writeWSAuthFrame(conn wsMessageWriter, payload []byte, frame string, attrs ...any) bool {
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		logAttrs := append([]any{"frame", frame, "error", err}, attrs...)
		slog.Warn("ws: failed to send auth frame", logAttrs...)
		return false
	}
	return true
}

// writeWSAuthErrorAndClose 给客户端回一帧鉴权失败消息（auth_error）然后关闭连接。
func writeWSAuthErrorAndClose(conn *websocket.Conn, payload []byte, attrs ...any) {
	writeWSAuthFrame(conn, payload, "auth_error", attrs...)
	conn.Close()
}

// HandleWebSocket 把一次 HTTP 请求升级成 WebSocket，并完成鉴权与入房。
//
// 鉴权路径有两条：优先看 Cookie（浏览器自动携带）；没有 Cookie 就用首条消息里的
// 令牌（移动端/CLI 等无法带 Cookie 的客户端）。两条路最终都要求该用户确实属于
// 目标 workspace，否则拒绝握手。
func HandleWebSocket(hub *Hub, mc MembershipChecker, pr PATResolver, resolveSlug SlugResolver, w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		if slug := r.URL.Query().Get("workspace_slug"); slug != "" && resolveSlug != nil {
			resolved, err := resolveSlug(r.Context(), slug)
			if err != nil {
				http.Error(w, `{"error":"workspace not found"}`, http.StatusNotFound)
				return
			}
			workspaceID = resolved
		}
	}
	if workspaceID == "" {
		http.Error(w, `{"error":"workspace_id or workspace_slug required"}`, http.StatusBadRequest)
		return
	}

	var userID string
	if cookie, err := r.Cookie(auth.CookieName(auth.AuthCookieName)); err == nil && cookie.Value != "" {
		uid, errMsg := authenticateToken(cookie.Value, pr, r.Context())
		if errMsg != "" {
			status := http.StatusUnauthorized
			if errMsg == `{"error":"account disabled"}` {
				status = http.StatusForbidden
			}
			http.Error(w, errMsg, status)
			return
		}
		if !mc.IsMember(r.Context(), uid, workspaceID) {
			http.Error(w, `{"error":"not a member of this workspace"}`, http.StatusForbidden)
			return
		}
		userID = uid
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}

	// 入站消息上限要在这里设、而不是等 readPump 里再设：下面的令牌鉴权路径要在身份
	// 认证完成前就读取首条帧，晚一步加限制，这次读取就没有任何大小保护了。
	conn.SetReadLimit(inboundReadLimit)

	if userID == "" {
		tokenStr, errMsg, closed := firstMessageAuth(conn)
		if closed {
			return
		}
		if errMsg != "" {
			writeWSAuthErrorAndClose(conn, []byte(errMsg), "workspace_id", workspaceID)
			return
		}
		uid, errMsg := authenticateToken(tokenStr, pr, r.Context())
		if errMsg != "" {
			writeWSAuthErrorAndClose(conn, []byte(errMsg), "workspace_id", workspaceID)
			return
		}
		if !mc.IsMember(r.Context(), uid, workspaceID) {
			writeWSAuthErrorAndClose(
				conn,
				[]byte(`{"error":"not a member of this workspace"}`),
				"workspace_id", workspaceID,
				"user_id", uid,
			)
			return
		}
		userID = uid

		if !writeWSAuthFrame(
			conn,
			[]byte(`{"type":"auth_ack"}`),
			"auth_ack",
			"workspace_id", workspaceID,
			"user_id", userID,
		) {
			conn.Close()
			return
		}
	}

	// 客户端元数据从 query 参数里取（浏览器在 WebSocket 升级请求里设不了自定义头，
	// WSClient 只能把它们拼进 URL）。每次连接都记一条，让 WS 和 HTTP 拥有同样的可观测维度。
	clientPlatform := r.URL.Query().Get("client_platform")
	clientVersion := r.URL.Query().Get("client_version")
	clientOS := r.URL.Query().Get("client_os")
	slog.Info("websocket connected",
		"user_id", userID,
		"workspace_id", workspaceID,
		"client_platform", clientPlatform,
		"client_version", clientVersion,
		"client_os", clientOS,
	)

	client := &Client{
		hub:         hub,
		conn:        conn,
		send:        make(chan []byte, 256),
		userID:      userID,
		workspaceID: workspaceID,
	}
	hub.register <- client

	go client.writePump()
	go client.readPump()
}

// inboundFrame 描述当前服务器能理解的入站 JSON 消息结构：type 标种类，
// payload 留成原始字节、等知道具体类型后再二次解析。
type inboundFrame struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// subPayload 是 subscribe / unsubscribe 消息的 payload 结构。
type subPayload struct {
	Scope string `json:"scope"`
	ID    string `json:"id"`
}

// readPump 是这条连接的「读循环」：持续读入站帧交给 handleFrame 处理。任何读错误
// （对端关闭、超时、超限）都会跳出循环，随后把连接注销并关闭底层连接。
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			switch {
			case errors.Is(err, websocket.ErrReadLimit):
				// 单独计数，让「单帧超限被关」保持可见，而不是混进普通连接的正常断连里。
				M.InboundTooLargeTotal.Add(1)
				slog.Warn("ws: inbound frame exceeded read limit",
					"limit_bytes", inboundReadLimit,
					"user_id", c.userID,
					"workspace_id", c.workspaceID,
				)
			case websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure):
				slog.Debug("websocket read error", "error", err, "user_id", c.userID, "workspace_id", c.workspaceID)
			}
			break
		}
		c.handleFrame(raw)
	}
}

// handleFrame 解析一条入站 JSON 帧并按其 type 分发：subscribe/unsubscribe 处理
// 订阅变更，ping 回 pong，未知 type 静默忽略（给未来的客户端协议留兼容余地）。
func (c *Client) handleFrame(raw []byte) {
	var f inboundFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		slog.Debug("ws inbound: invalid json", "error", err, "user_id", c.userID)
		return
	}
	switch f.Type {
	case "subscribe", "unsubscribe":
		var p subPayload
		if err := json.Unmarshal(f.Payload, &p); err != nil || p.Scope == "" || p.ID == "" {
			c.sendJSON(map[string]any{
				"type": f.Type + "_error",
				"payload": map[string]string{
					"scope": p.Scope,
					"id":    p.ID,
					"error": "invalid payload",
				},
			})
			return
		}
		if f.Type == "subscribe" {
			c.handleSubscribe(p.Scope, p.ID)
		} else {
			c.handleUnsubscribe(p.Scope, p.ID)
		}
	case "ping":
		c.sendJSON(map[string]string{"type": "pong"})
	default:
		// 未知帧类型——为前向兼容静默忽略（打 debug 日志，不报错不计数）。
		slog.Debug("ws inbound: unknown frame", "type", f.Type, "user_id", c.userID)
	}
}

// handleSubscribe 处理一次订阅请求：按作用域类型分流做不同等级的校验。
func (c *Client) handleSubscribe(scope, id string) {
	switch scope {
	case ScopeWorkspace, ScopeUser:
		// 这两个是「隐性作用域」：只允许订阅与自己连接身份一致的那个，防止偷听别人
		// 的 workspace/user 房间。
		if (scope == ScopeWorkspace && id != c.workspaceID) || (scope == ScopeUser && id != c.userID) {
			M.SubscribeDeniedTotal(scope).Add(1)
			c.sendJSON(map[string]any{
				"type": "subscribe_error",
				"payload": map[string]string{
					"scope": scope,
					"id":    id,
					"error": "forbidden",
				},
			})
			return
		}
		// 连接建立时已经自动订阅过这两个作用域；这里再调 subscribe 幂等，只回 ack。
		c.hub.subscribe(c, scope, id)
	case ScopeTask, ScopeChat:
		auth := c.hub.authorizer
		if auth != nil {
			ok, err := auth.AuthorizeScope(context.Background(), c.userID, c.workspaceID, scope, id)
			if err != nil || !ok {
				M.SubscribeDeniedTotal(scope).Add(1)
				reason := "forbidden"
				if err != nil {
					reason = "lookup_failed"
				}
				c.sendJSON(map[string]any{
					"type": "subscribe_error",
					"payload": map[string]string{
						"scope": scope,
						"id":    id,
						"error": reason,
					},
				})
				return
			}
		}
		c.hub.subscribe(c, scope, id)
	default:
		M.SubscribeDeniedTotal(scope).Add(1)
		c.sendJSON(map[string]any{
			"type": "subscribe_error",
			"payload": map[string]string{
				"scope": scope,
				"id":    id,
				"error": "unknown_scope",
			},
		})
		return
	}
	c.sendJSON(map[string]any{
		"type":    "subscribe_ack",
		"payload": map[string]string{"scope": scope, "id": id},
	})
}

// handleUnsubscribe 处理一次退订请求：把它从目标房间摘掉，回一个 unsubscribe_ack。
func (c *Client) handleUnsubscribe(scope, id string) {
	c.hub.unsubscribe(c, scope, id)
	c.sendJSON(map[string]any{
		"type":    "unsubscribe_ack",
		"payload": map[string]string{"scope": scope, "id": id},
	})
}

// sendJSON 尽力把 v 编码成 JSON 并塞进该客户端的发送队列。队列已满时直接丢弃这条
// 消息（下一步广播循环会把写不动的连接当成慢客户端剔除）。
func (c *Client) sendJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
	}
}

// writePump 是这条连接的「写循环」：串行消费发送队列里的消息写到 socket 上，并
// 周期性发送 ping 保活。写失败或发送队列被关（连接被剔除）就关闭底层连接收尾。
func (c *Client) writePump() {
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
				slog.Warn("websocket write error", "error", err, "user_id", c.userID, "workspace_id", c.workspaceID)
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
