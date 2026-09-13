// Package dispatch 存放"执行准入/分派结果"的规范、跨层共享词汇表（MUL-4525）。
// 它是叶子包（无 internal 依赖），因此服务层——它做出准入/跳过决定、在源头持有原因——
// 与 handler 层——它把原因序列化到线路上——共用同一个枚举，永远不会漂移。
//
// ReasonCode 在阻止/跳过某次运行的判断分支处决定，并原样带到响应中；绝不允许从
// 可读的失败字符串反推。码值稳定、可被客户端本地化，且对枚举安全：一个码值不会
// 泄露私有 agent 是否存在、其名字或其所有者。
package dispatch

// ReasonCode 是一个稳定、可供客户端本地化的准入/分派原因码。
type ReasonCode string

const (
	// ReasonQueued / ReasonCoalesced / ReasonDeferred 是成功路径的码值。
	ReasonQueued    ReasonCode = "queued"
	ReasonCoalesced ReasonCode = "coalesced"
	ReasonDeferred  ReasonCode = "deferred"

	// ReasonInvocationNotAllowed：在"调用权限"模型下，发起主体不允许触发该目标。
	// 刻意保持通用——不区分"目标是私有的"与"目标不存在"。
	ReasonInvocationNotAllowed ReasonCode = "invocation_not_allowed"
	// ReasonTargetUnavailable：目标无法运行（agent 已归档、squad 已删除/归档、
	// leader 无法解析，或没有受理人）。
	ReasonTargetUnavailable ReasonCode = "target_unavailable"
	// ReasonRuntimeOffline：目标有权限且绑定了 runtime，但分派时该 runtime 不在线。
	// 任务不会丢——用户的修复办法是把机器带回来，排队的工作会等它。
	ReasonRuntimeOffline ReasonCode = "runtime_offline"
	// ReasonRuntimeUnusable：目标绑定的 runtime 机器可达，但其上的 agent CLI 无法
	// 执行——实际案例是包 postinstall 被阻断后留下的 npm 占位 stub（MUL-6164）。
	// 之所以区别于 runtime_offline，和 agent_runtime_required 需要单独区分的原因一样：
	// 这里等待无济于事——机器已经在线，修复方式是用户到机器上执行一条命令；daemon
	// 用这个判定把情况报回来，让客户端可以直接展示。
	ReasonRuntimeUnusable ReasonCode = "runtime_unusable"
	// ReasonAgentRuntimeRequired：目标有权限但完全没绑定 runtime（agent.runtime_id
	// IS NULL）——runtime 被删除后 agent 就落在这个状态（MUL-5559）。刻意与
	// runtime_offline 区分：没有机器可带回，永远不会有人认领该 agent 的工作，唯一
	// 的修复是把它绑到某个 runtime。把两者混为一谈的客户端会让用户去找一台并不存在
	// 的离线电脑。
	ReasonAgentRuntimeRequired ReasonCode = "agent_runtime_required"
	// ReasonAttributionBlocked：一个"失败即关闭"的工作区无法为该次运行解析出负责的
	// 人类，因此运行被拒绝。
	ReasonAttributionBlocked ReasonCode = "attribution_blocked"
	// ReasonAlreadyActive：该目标已有进行中/待处理的运行，而本次触发没有合并进去。
	ReasonAlreadyActive ReasonCode = "already_active"
	// ReasonSelfTriggerSuppressed：目标被有意不（重新）触发，因为那会构成防护要抑制的
	// 自我触发，且没有尚在活跃的运行兜底——例如 squad leader 自己 @自己的 squad，
	// 而其最新任务已处于终端状态。这不是权限阻断，但也不算成功：没有新东西运行。
	//（命名避免暗示新评论已被处理。）
	ReasonSelfTriggerSuppressed ReasonCode = "self_trigger_suppressed"
	// ReasonQuotaExceeded 是对已耗尽的 Cloud 提供的 autopilot 间隔所作的中性拒绝。
	ReasonQuotaExceeded ReasonCode = "quota_exceeded"
	// ReasonIssueLimitReached 表示 create_issue 的 Autopilot 被准入执行，但 Cloud
	// 的有效工作区 issue 数量上限拦下了该 issue。
	ReasonIssueLimitReached ReasonCode = "issue_limit_reached"
	// ReasonInternalError：意外的服务端错误导致无法给出干净的决定。
	ReasonInternalError ReasonCode = "internal_error"
)
