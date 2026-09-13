// 本文件定义 realtime 包里「广播的抽象层」：Scope 常量 + Broadcaster /
// Deliverer 接口。作用是让事件生产方（别处代码）只依赖接口、不依赖 *Hub
// 具体实现，方便测试替换；Scope 常量则统一收编「一条消息应该发给谁」的划分。
package realtime

// broadcaster 认识的所有 Scope（作用域）类型。Scope 决定一条实时消息进哪个
// 房间：生产方和消费方必须都用这些常量而不是手写字符串，否则写错一个字就
// 会把事件静默发到不存在的房间、没有任何人收到。
const (
	ScopeWorkspace = "workspace"
	ScopeUser      = "user"
	ScopeTask      = "task"
	ScopeChat      = "chat"

	// ScopeDaemonRuntime 让「daemon 唤醒帧」走 Redis relay 转发。它由 daemon
	// 自己的 WebSocket hub 消费，浏览器端客户端不接收。
	ScopeDaemonRuntime = "daemon_runtime"

	// ScopeWecomOutbound 把一条企业微信(WeCom)回复路由到「持有该安装实例
	// aibot WebSocket 的那台副本进程」。
	//
	// 背景：WeCom 是唯一一个「出站消息没有 REST 路径」的通道——回复只能从
	// 持有 socket 的那个进程发出去，而 WS lease 保证同一时刻只有恰好一个进程
	// 持有它。但 chat:done 事件是由「服务 daemon 完成回调的那台副本」发到进程
	// 内 events.Bus 的，而选哪台是负载均衡的决定，跟谁持有 socket 无关。以前
	// 非持锁副本收到时回复直接被丢弃（GH #7215、#6890，以及 SELF_HOSTING.md
	// 里写的单副本例外）。
	//
	// 它的 scope id 是安装实例 id，所以形态和 ScopeDaemonRuntime 一样：真正要
	// 干活的东西只活在某台特定副本上，其它副本靠 relay 转发才能找到它。
	ScopeWecomOutbound = "wecom_outbound"
)

// Broadcaster 是每个 realtime 事件生产方都应依赖的抽象，而不是直接依赖
// *Hub。
//
// 一期改造（MUL-1138）在接口上扩展出 BroadcastToScope，让事件能按高频的
// 单资源作用域（`task:{id}`、`chat:{id}`）扇出，而不必每次扇给整个
// workspace。老方法继续可用，底层现在都改走 BroadcastToScope。
type Broadcaster interface {
	// BroadcastToScope 把一条消息扇给本节点上所有当前订阅了
	// ({scopeType, scopeID}) 的连接。
	BroadcastToScope(scopeType, scopeID string, message []byte)

	// BroadcastToWorkspace 是 BroadcastToScope("workspace", workspaceID,
	// message) 的向后兼容简写。
	BroadcastToWorkspace(workspaceID string, message []byte)

	// SendToUser 是 BroadcastToScope("user", userID, message) 的向后兼容简写。
	// 可选的 excludeWorkspace 参数是为 `member:added` 去重路径保留的：
	// workspaceID 命中该值的连接会被跳过、不重复推送。
	SendToUser(userID string, message []byte, excludeWorkspace ...string)

	// Broadcast 把一条消息扇给本节点上的所有连接。用于没有 workspace
	// 概念的 daemon:* 事件。
	Broadcast(message []byte)
}

// DaemonRuntimeDeliverer 消费 daemon-runtime 作用域的 relay 帧。
type DaemonRuntimeDeliverer interface {
	DeliverDaemonRuntime(scopeID string, frame []byte, eventID string)
}

// WecomOutboundDeliverer 消费 wecom-outbound 作用域的 relay 帧。每台副本都会
// 收到每一帧；实现方只在「自己持有该安装实例的 socket」时才投递、其余全部
// 忽略，因此最终恰好只有一台真正把回复发出去。
type WecomOutboundDeliverer interface {
	DeliverWecomOutbound(scopeID string, frame []byte, eventID string)
}

// 编译期断言：*Hub 依然满足 Broadcaster 接口。只要 Hub 的方法集缺了任何
// 一个接口方法，这一行就会编译报错。
var _ Broadcaster = (*Hub)(nil)
