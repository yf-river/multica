// Package taskfailure 是写进 agent_task_queue.failure_reason 与
// chat_message.failure_reason 的「规范且细分」取值体系。
//
// 历史：在 MUL-1949 之前，server/daemon 代码只写一小撮粗糙的
// failure_reason（"agent_error"、"timeout"、"runtime_offline"……）。其中
// "agent_error" 一桶占了全部失败的约 30%，把真正的根因（provider 401、
// 配额耗尽、上下文溢出、runner 崩溃等）都藏在了自由文本的 `error` 列里。
// MUL-1949 的离线回填 SQL 用一段 CASE 表达式按错误文本把这批行重新归类成
// 14 个 agent_error.* 子原因。
//
// 本包把那个分类器提升到「在途写入路径」上，让 failure_reason 在行第一次落库
// 时就已经是细分的；同时让 server / daemon / cloud 对「规范取值」共享唯一事实
// 来源。这是 Grafana 面板计划的 PR1（[MUL-2946](https://multica/issues/MUL-2946)），
// 后续 PR 用 AllReasons() 预热 Prometheus 的 failure_reason 标签集。
//
// 规范值分两组：
//
//   - 平台侧值（无 `agent_error.` 前缀），由 server 侧清扫器与 daemon 分类器在
//     失败可归因于平台/调度/运行时层、而非 agent 进程自身行为时写入：
//
//     queued_expired, runtime_offline, runtime_reconnect_timeout,
//     runtime_recovery, timeout, iteration_limit, agent_blocked,
//     api_invalid_request, skill_bundle_unavailable,
//     runtime_cli_timeout, invalid_task_identity
//
//   - 14 个 agent 侧值（带 `agent_error.` 前缀），由 Classify(rawError) 在 agent
//     进程给出一个错误字符串时产出。IsAgentError 报告某值是否属于这一组。
//
// 线格式稳定性：这些常量的字符串形态会被持久化进数据库，并作为 Prometheus
// 标签暴露。重命名一个取值属于破坏性变更。新增分类器导出的取值，还需要配一段
// 等价的 SQL 回填规则；平台侧事件可以直接新增，因为没有历史行需要重新归类。
package taskfailure

import "strings"

// Reason 是 failure_reason 取值的字符串底层枚举，对应存进
// agent_task_queue.failure_reason 的规范值。请用 Reason* 常量而不是裸字符串
// 字面量，这样编译器能抓出拼写错误，将来整个分类体系要演进也能包级统一改。
type Reason string

// agentErrorPrefix 标记 14 个源自 agent 进程内部的子原因（provider 错误、runner
// 崩溃、上下文溢出等），以区别于平台侧原因（队列过期、runtime 离线、清扫器
// 超时等）。IsAgentError 靠这个前缀判断，调用方就不用手工枚举所有 agent 侧原因。
const agentErrorPrefix = "agent_error."

const (
	// 平台 / 调度器侧：失败归因于 Multica 基础设施，而非 agent 进程的任何行为。
	// 这些由 server 侧清扫器（ExpireStaleQueuedTasks、FailStaleTasks、
	// FailTasksForOfflineRuntimes、RecoverOrphanedTasksForRuntime）与 daemon 的
	// 中毒会话分类器（api_invalid_request、iteration_limit、agent_blocked）写入。
	// IsAgentError 对它们一律返回 false。

	// ReasonQueuedExpired：任务在 'queued' 状态超过 TTL 仍未被认领
	// （典型是 autopilot 积压而受让人 runtime 离线）。由 ExpireStaleQueuedTasks 写入。
	ReasonQueuedExpired Reason = "queued_expired"

	// ReasonRuntimeOffline：持有已派发/运行中任务的 runtime 掉线了。
	// 由 FailTasksForOfflineRuntimes 写入。
	ReasonRuntimeOffline Reason = "runtime_offline"

	// ReasonRuntimeReconnectTimeout：为等待离线 runtime 而延迟的重试，
	// 在整个重连宽限期后仍未完成。不可重试：runtime 必须先回来，
	// 用户才能发起下一次尝试。由 FailExpiredRuntimeReconnectRetries 写入。
	ReasonRuntimeReconnectTimeout Reason = "runtime_reconnect_timeout"

	// ReasonRuntimeRecovery：任务在途时 daemon 重启了，之前的会话不可恢复。
	// 由 RecoverOrphanedTasksForRuntime 在 daemon 启动时写入。
	ReasonRuntimeRecovery Reason = "runtime_recovery"

	// ReasonTimeout：server 侧或 runtime 侧的硬超时。
	// 由 FailStaleTasks（server）与 daemon 的每任务 agent 超时路径写入。
	ReasonTimeout Reason = "timeout"

	// ReasonIterationLimit：agent 达到了单次运行的迭代上限，吐出一条兜底的
	// "I reached the iteration limit" 消息。归类到平台侧，因为这是 Multica
	// 强加的预算，而不是外部 API 的拒绝。
	ReasonIterationLimit Reason = "iteration_limit"

	// ReasonAgentBlocked：agent 主动进入了 'blocked' 工作流状态
	// （例如请求人工输入）。不是系统错误。
	ReasonAgentBlocked Reason = "agent_blocked"

	// ReasonAPIInvalidRequest：上游 LLM API 以 400 invalid_request_error 拒绝了
	// 请求体（图片过大、payload 畸形等）。会话历史本身已被污染——同一会话上
	// 的下一个任务会重放同一个 400——所以 GetLastTaskSession 会把该原因排除在
	// resume 查找之外。由 classifyPoisonedError 写入（daemon/poisoned.go）。
	ReasonAPIInvalidRequest Reason = "api_invalid_request"

	// ReasonSkillBundleUnavailable：任务准备期间 daemon 无法从控制面下载
	// agent 的 skill 包——agent 进程从未被拉起。归为平台侧，因为不是 agent
	// 的任何行为导致的：常见触发是到 API 的链路慢/被堵，或 daemon 没继承到
	// 代理配置（MUL-5370）。可重试，而且重试成本低：已经到达的每个包都缓存
	// 在磁盘上，连续尝试是收敛的而不是整套重新下载。由 taskRunFailureReason
	// 写入（daemon/daemon.go）。
	ReasonSkillBundleUnavailable Reason = "skill_bundle_unavailable"

	// ReasonRuntimeCLITimeout：任务准备期间需要的本地 runtime CLI 未在期限内
	// 应答。agent 进程从未被拉起、也没接触任何 provider；本地 runtime 一直
	// 不可用时重试没有意义。
	ReasonRuntimeCLITimeout Reason = "runtime_cli_timeout"

	// ReasonInvalidTaskIdentity：daemon 拒绝了一个已认领的任务，因为任务行的
	// 权威 agent_id 缺失、或与嵌套的 agent payload 不一致。agent 进程从不启动。
	// 刻意不可重试：重试同一个自相矛盾的认领只会重复一次隔离失败。
	ReasonInvalidTaskIdentity Reason = "invalid_task_identity"

	// Agent 进程侧：由 agent CLI / SDK 以错误字符串形式浮现的失败。
	// Classify(rawError) 负责从字符串里挑出正确的子原因。IsAgentError 对
	// 这些一律返回 true。

	// ReasonAgentProviderAuthOrAccess：401 / 403、"Not logged in"、API key 无效、
	// 无该模型访问权限。不可重试；用户必须重新授权。
	ReasonAgentProviderAuthOrAccess Reason = "agent_error.provider_auth_or_access"

	// ReasonAgentProviderQuotaLimit：402、insufficient_balance、月度用量上限、
	// 积分耗尽。账户充值前不可重试。
	ReasonAgentProviderQuotaLimit Reason = "agent_error.provider_quota_limit"

	// ReasonAgentProviderCapacityOrRateLimit：429 / 529、被限流、过载、无可用
	// 容量。瞬时——退避 + 重试是合适的。
	ReasonAgentProviderCapacityOrRateLimit Reason = "agent_error.provider_capacity_or_rate_limit"

	// ReasonAgentProviderServerError：provider 5xx、internal error、service
	// unavailable、bad gateway。瞬时——短退避。
	ReasonAgentProviderServerError Reason = "agent_error.provider_server_error"

	// ReasonAgentProviderNetwork：流断开、dial tcp 失败、DNS 失败、i/o timeout。
	// 瞬时。
	ReasonAgentProviderNetwork Reason = "agent_error.provider_network"

	// ReasonAgentProcessFailure：agent 子进程以非零码退出、崩溃，或返回了意外
	// 信号。runner / 后端质量问题。
	ReasonAgentProcessFailure Reason = "agent_error.process_failure"

	// ReasonAgentEmptyOrUnparseableOutput：agent CLI 返回空输出，或返回了按它
	// 已知协议解析不了的内容。包装层 / 协议健壮性问题。
	ReasonAgentEmptyOrUnparseableOutput Reason = "agent_error.empty_or_unparseable_output"

	// ReasonAgentTimeout：agent 子进程撞上硬超时（例如 2h）——区别于
	// ReasonTimeout（平台侧清扫器的超时）。
	ReasonAgentTimeout Reason = "agent_error.agent_timeout"

	// ReasonAgentContextOverflow：提示词或上下文窗口超出模型上限。同一会话上
	// 不可重试；需要压缩或开新会话。
	ReasonAgentContextOverflow Reason = "agent_error.context_overflow"

	// ReasonAgentMissingConfig：agent / runtime 缺一个必需的环境变量或 API key，
	// 或根本没配 LLM provider。
	ReasonAgentMissingConfig Reason = "agent_error.missing_config"

	// ReasonAgentModelNotFoundOrUnavailable：所选模型 id 不存在、对该账号不可用，
	// 或 provider 返回了 HTTP 404。
	ReasonAgentModelNotFoundOrUnavailable Reason = "agent_error.model_not_found_or_unavailable"

	// ReasonAgentRuntimeVersionUnsupported：本地 runner CLI 低于最低支持版本，
	// 或协议不兼容。
	ReasonAgentRuntimeVersionUnsupported Reason = "agent_error.runtime_version_unsupported"

	// ReasonAgentRuntimeMissingExecutable：runner CLI 二进制没装 / 不在 PATH。
	ReasonAgentRuntimeMissingExecutable Reason = "agent_error.runtime_missing_executable"

	// ReasonAgentUnknown：分类器没匹配上任何规则。这个桶预期保持很小
	// （占失败任务 <5%）——占比上升是分类器需要新规则的信号。Classify 在
	// 没有任何其它规则命中时（包括空输入）返回它。
	ReasonAgentUnknown Reason = "agent_error.unknown"
)

// allReasons 是 25 个原因的规范有序列表。顺序稳定，调用方（例如用 AllReasons
// 预热时序的 Prometheus 采集器）跨重启都能得到确定性的标签集。
//
// 排序规则：
//  1. 平台侧原因按任务生命周期中大致触发的先后（入队 → 派发 → 运行 → 收尾）。
//  2. agent 侧原因按职责领域分组（provider / agent 进程 / 配置 / runtime），
//     最后放 catchall。
var allReasons = []Reason{
	// 平台 / 调度器侧。
	ReasonQueuedExpired,
	ReasonRuntimeOffline,
	ReasonRuntimeReconnectTimeout,
	ReasonRuntimeRecovery,
	ReasonTimeout,
	ReasonIterationLimit,
	ReasonAgentBlocked,
	ReasonAPIInvalidRequest,
	ReasonSkillBundleUnavailable,
	ReasonRuntimeCLITimeout,
	ReasonInvalidTaskIdentity,

	// Agent 进程侧：provider 错误。
	ReasonAgentProviderAuthOrAccess,
	ReasonAgentProviderQuotaLimit,
	ReasonAgentProviderCapacityOrRateLimit,
	ReasonAgentProviderServerError,
	ReasonAgentProviderNetwork,

	// Agent 进程侧：agent / runner 错误。
	ReasonAgentProcessFailure,
	ReasonAgentEmptyOrUnparseableOutput,
	ReasonAgentTimeout,
	ReasonAgentContextOverflow,
	ReasonAgentMissingConfig,
	ReasonAgentModelNotFoundOrUnavailable,
	ReasonAgentRuntimeVersionUnsupported,
	ReasonAgentRuntimeMissingExecutable,

	// Catchall。
	ReasonAgentUnknown,
}

// String 返回原因的线格式形态——即写进 failure_reason 列、并作为 Prometheus
// 标签值暴露的那个字符串。
func (r Reason) String() string { return string(r) }

// IsAgentError 报告原因是否源自 agent 进程内部（provider 错误、runner 崩溃、
// 上下文溢出等），而非平台/调度器/运行时层（队列过期、runtime 离线、清扫器
// 超时等）。
//
// 判定刻意基于字符串前缀而非枚举成员测试：将来新增任何 agent_error.* 值都会
// 自动获得正确的分组，无需更新这个方法。
func (r Reason) IsAgentError() bool {
	return strings.HasPrefix(string(r), agentErrorPrefix)
}

// AllReasons 按稳定顺序返回全部规范原因。调用方绝不能改动返回的切片——
// 这里返回拷贝，并发调用方可以在自己的副本上 append 而不破坏包级底稿。
//
// 主要用途：预热 Prometheus 的 failure_reason 标签，让生产进程还没见过的标签
// 也以零值时序可观测，而不是等到该类失败第一次出现才冒出来。
func AllReasons() []Reason {
	out := make([]Reason, len(allReasons))
	copy(out, allReasons)
	return out
}
