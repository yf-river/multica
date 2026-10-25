// 本文件承载「PR2 业务指标」的主干：agent 任务从入队到终态的生命周期计数与耗时、
// LLM token/成本、runtime 清扫器（sweeper）/GC、entitlement（订阅权益）判定等。
// 这是整个 multica_* 指标里维度最多的一组；指标定义与登记表（标签集）分离，
// 后者在 labels.go。PR3 那批漏斗/商业事件在 business_events.go。
package metrics

import (
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
	"github.com/prometheus/client_golang/prometheus"
)

// taskDurationBuckets 是任务队列等待/运行/总耗时的直方图桶（秒），覆盖亚秒级到 2 小时。
var taskDurationBuckets = []float64{1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1200, 3600, 7200}

// chatClaimResumeQueryDurationBuckets 会话恢复查询的耗时桶（秒），毫秒级到秒级。
// 这类查询（回读最近会话/历史）预期很快，桶取到 5s 是为了观察慢查询的尾巴。
var chatClaimResumeQueryDurationBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

// runtimeSweepStageDurationBuckets runtime 维护清扫器各阶段的耗时桶（秒），
// 单阶段预期在毫秒级，最长放到 60s 覆盖异常慢扫。
var runtimeSweepStageDurationBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15, 30, 60}

// Runtime 清扫器把每次清扫拆成一系列有固定名字的阶段，分别上报耗时/候选行数/变更行数；
// 下面是合法的阶段名常量（配合 normalizeRuntimeSweepStage 使用）：
const (
	RuntimeSweepStageLiveness                 = "runtime_liveness"
	RuntimeSweepStageOfflineTasks             = "offline_runtime_tasks"
	RuntimeSweepStageReconnectRetries         = "runtime_reconnect_retries"
	RuntimeSweepStageStaleTasks               = "stale_tasks"
	RuntimeSweepStageQueuedExpiry             = "queued_task_expiry"
	RuntimeSweepStageDelegatedFailureRecovery = "delegated_failure_recovery"
	RuntimeSweepStageDeferredChatFinalization = "deferred_chat_finalize"
	RuntimeSweepStageGC                       = "runtime_gc"

	// RuntimeGC 跳过候选时的原因常量（配合 normalizeRuntimeGCSkipReason）：
	RuntimeGCSkipEligibilityChanged = "eligibility_changed"
	RuntimeGCSkipNonTerminalTask    = "non_terminal_task"
	RuntimeGCSkipWorkspaceMismatch  = "workspace_mismatch"
)

// activeTaskLabels 记录「还在执行中」的任务在 in_progress 指标上的标签。
// 用于在任务真正结束时精确地给对应的 (source, runtime_mode) 组合做 -1。
type activeTaskLabels struct {
	source      string
	runtimeMode string
}

// BusinessMetrics 是业务指标（PR2 + PR3 两批）的总句柄。它内部装着若干 CounterVec /
// HistogramVec / GaugeVec，通过 Collectors() 一次注册；业务代码拿实例后调用下面的
// Record* / Observe* 方法记数。绝大多数方法都容忍 nil 接收者，方便调用方在
// 「指标未初始化」时安全跳过。
type BusinessMetrics struct {
	// --- agent 任务生命周期（agent_task 子系统） ---
	taskEnqueued      *prometheus.CounterVec
	taskDispatched    *prometheus.CounterVec
	taskStarted       *prometheus.CounterVec
	taskTerminal      *prometheus.CounterVec
	taskFailed        *prometheus.CounterVec
	taskQueueWait     *prometheus.HistogramVec
	taskRunSeconds    *prometheus.HistogramVec
	taskTotalSeconds  *prometheus.HistogramVec
	taskInProgress    *prometheus.GaugeVec
	taskIterations    *prometheus.HistogramVec
	taskClaimDecision *prometheus.CounterVec

	// --- LLM token 与成本（llm 子系统） ---
	llmTokens         *prometheus.CounterVec
	llmCostUSD        *prometheus.CounterVec
	llmUnpricedTokens *prometheus.CounterVec
	llmRequests       *prometheus.CounterVec

	// --- 任务调度异常 / 聊天接管 / runtime 维护 ---
	taskQueuedExpired              *prometheus.CounterVec
	taskLeaseExpired               *prometheus.CounterVec
	chatClaimSessionFallbackNeeded prometheus.Counter
	chatClaimSessionFallbackResult *prometheus.CounterVec
	chatClaimResumeQueryDuration   *prometheus.HistogramVec
	runtimeSweepStageDuration      *prometheus.HistogramVec
	runtimeSweepCandidateRows      *prometheus.CounterVec
	runtimeSweepRowsChanged        *prometheus.CounterVec
	runtimeGCDeleted               prometheus.Counter
	runtimeGCFailed                prometheus.Counter
	runtimeGCSkipped               *prometheus.CounterVec

	// agentRuntimeLookup 统计按「产品来源」拆分的 agent_runtime 单行查询。所有来源共用
	// 同一条 SQL 指纹，所以只有这里能区分 daemon 心跳、浏览器轮询、就绪门禁各占多少。
	// 来源/结果的封闭枚举见 labels.go。
	agentRuntimeLookup *prometheus.CounterVec

	// activeMu 保护 activeTasks；activeTasks 是「已分发但未终态」的任务表，
	// 用来让 in_progress Gauge 在任务结束时精确减掉对应标签的计数（而非笼统 -1）。
	activeMu    sync.Mutex
	activeTasks map[string]activeTaskLabels

	// PR3 漏斗/社区/商业计数器。字段级说明与标签见 business_events.go。
	events *businessEventMetrics
}

// NewBusinessMetrics 创建业务指标全集（PR2 + PR3）。流程：
//  1. 先跑标签自检 validateBusinessMetricLabels，把「指标挂了高基数标签」拦在启动时；
//  2. 逐个构造计数器/直方图/Gauge，标签取自 labels.go 的登记表；
//  3. 预热常见系列：失败原因全网格、runtime GC 跳过原因、agent_runtime 查询的
//     「来源 × 结果」全网格，让没触发过的维度在图表上是 0 而不是查无此序列。
func NewBusinessMetrics() *BusinessMetrics {
	validateBusinessMetricLabels()
	m := &BusinessMetrics{
		taskEnqueued: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "agent_task",
			Name:      "enqueued_total",
			Help:      "Total agent tasks enqueued.",
		}, metricLabels("multica_agent_task_enqueued_total")),
		taskDispatched: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "agent_task",
			Name:      "dispatched_total",
			Help:      "Total agent tasks dispatched to a runtime.",
		}, metricLabels("multica_agent_task_dispatched_total")),
		taskStarted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "agent_task",
			Name:      "started_total",
			Help:      "Total agent tasks that reached running state.",
		}, metricLabels("multica_agent_task_started_total")),
		taskTerminal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "agent_task",
			Name:      "terminal_total",
			Help:      "Total agent tasks that reached a terminal state.",
		}, metricLabels("multica_agent_task_terminal_total")),
		taskFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "agent_task",
			Name:      "failed_total",
			Help:      "Total failed agent tasks by canonical failure reason.",
		}, metricLabels("multica_agent_task_failed_total")),
		taskQueueWait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "multica",
			Subsystem: "agent_task",
			Name:      "queue_wait_seconds",
			Help:      "Time agent tasks spent queued before dispatch.",
			Buckets:   taskDurationBuckets,
		}, metricLabels("multica_agent_task_queue_wait_seconds")),
		taskRunSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "multica",
			Subsystem: "agent_task",
			Name:      "run_seconds",
			Help:      "Time agent tasks spent running before a terminal state.",
			Buckets:   taskDurationBuckets,
		}, metricLabels("multica_agent_task_run_seconds")),
		taskTotalSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "multica",
			Subsystem: "agent_task",
			Name:      "total_seconds",
			Help:      "Total time from agent task creation to terminal state.",
			Buckets:   taskDurationBuckets,
		}, metricLabels("multica_agent_task_total_seconds")),
		taskInProgress: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "multica",
			Subsystem: "agent_task",
			Name:      "in_progress",
			Help:      "Current agent tasks dispatched by this process and not yet terminal.",
		}, metricLabels("multica_agent_task_in_progress")),
		taskIterations: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "multica",
			Subsystem: "agent_task",
			Name:      "iteration_count",
			Help:      "Retry attempt count observed when an agent task reaches a terminal state.",
			Buckets:   []float64{1, 2, 3, 4, 5, 10},
		}, metricLabels("multica_agent_task_iteration_count")),
		taskClaimDecision: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "agent_task",
			Name:      "claim_decision_total",
			Help:      "Claim decisions by claim path, outcome, and reason; one increment per claim attempt or per poll-level decision that never enters a claim attempt.",
		}, metricLabels("multica_agent_task_claim_decision_total")),
		llmTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "llm",
			Name:      "tokens_total",
			Help:      "Total priced LLM tokens by provider, model, token type, runtime mode, and task source.",
		}, metricLabels("multica_llm_tokens_total")),
		llmCostUSD: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "llm",
			Name:      "cost_usd_total",
			Help:      "Total estimated priced LLM token cost in USD.",
		}, metricLabels("multica_llm_cost_usd_total")),
		llmUnpricedTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "llm",
			Name:      "unpriced_tokens_total",
			Help:      "Total LLM tokens for model aliases without a fixed TSR price.",
		}, metricLabels("multica_llm_unpriced_tokens_total")),
		llmRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "llm",
			Name:      "request_total",
			Help:      "Total task usage reports by normalized LLM provider and model.",
		}, metricLabels("multica_llm_request_total")),
		taskQueuedExpired: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "task",
			Name:      "queued_expired_total",
			Help:      "Total queued tasks expired by the scheduler.",
		}, metricLabels("multica_task_queued_expired_total")),
		taskLeaseExpired: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "task",
			Name:      "lease_expired_total",
			Help:      "Total dispatched or running task leases expired by the scheduler.",
		}, metricLabels("multica_task_lease_expired_total")),
		chatClaimSessionFallbackNeeded: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "chat_claim",
			Name:      "session_fallback_needed_total",
			Help:      "Total chat claims whose session pointer lacked a provider session or workdir.",
		}),
		chatClaimSessionFallbackResult: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "chat_claim",
			Name:      "session_fallback_result_total",
			Help:      "Total chat-claim session fallback query results (hit, miss, or error).",
		}, metricLabels("multica_chat_claim_session_fallback_result_total")),
		chatClaimResumeQueryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "multica",
			Subsystem: "chat_claim",
			Name:      "resume_query_duration_seconds",
			Help:      "Duration of chat-claim resume-history queries by fixed query name.",
			Buckets:   chatClaimResumeQueryDurationBuckets,
		}, metricLabels("multica_chat_claim_resume_query_duration_seconds")),
		runtimeSweepStageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "multica",
			Subsystem: "runtime_sweeper",
			Name:      "stage_duration_seconds",
			Help:      "Duration of each runtime maintenance sweeper stage.",
			Buckets:   runtimeSweepStageDurationBuckets,
		}, metricLabels("multica_runtime_sweeper_stage_duration_seconds")),
		runtimeSweepCandidateRows: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "runtime_sweeper",
			Name:      "candidate_rows_total",
			Help:      "Total candidate rows returned to or examined by the application in each runtime maintenance sweeper stage.",
		}, metricLabels("multica_runtime_sweeper_candidate_rows_total")),
		runtimeSweepRowsChanged: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "runtime_sweeper",
			Name:      "rows_changed_total",
			Help:      "Total rows whose persisted maintenance state changed in each runtime sweeper stage.",
		}, metricLabels("multica_runtime_sweeper_rows_changed_total")),
		runtimeGCDeleted: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "runtime_gc",
			Name:      "deleted_total",
			Help:      "Total stale offline runtimes safely deleted by garbage collection.",
		}),
		runtimeGCFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "runtime_gc",
			Name:      "failed_total",
			Help:      "Total runtime garbage-collection operations that failed.",
		}),
		runtimeGCSkipped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "runtime_gc",
			Name:      "skipped_total",
			Help:      "Total runtime garbage-collection candidates safely skipped by reason.",
		}, metricLabels("multica_runtime_gc_skipped_total")),
		agentRuntimeLookup: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "agent_runtime", Name: "lookup_total",
			Help: "Total single-row agent_runtime reads by product source and outcome.",
		}, metricLabels("multica_agent_runtime_lookup_total")),
		activeTasks: map[string]activeTaskLabels{},
		events:      newBusinessEventMetrics(),
	}
	m.prewarmFailureReasons()
	m.prewarmClaimDecisions()
	for _, reason := range []string{RuntimeGCSkipEligibilityChanged, RuntimeGCSkipNonTerminalTask, RuntimeGCSkipWorkspaceMismatch} {
		m.runtimeGCSkipped.WithLabelValues(reason).Add(0)
	}
	// 预热「来源 × 结果」全网格（45 个序列），让本进程启动后尚未触发过的来源显示为 0
	// 而不是缺一条序列——对不存在的序列做 rate() 什么也不返回，这在看板上与
	// 「那条路径从来没埋点」无法区分。
	for _, source := range AllRuntimeLookupSources() {
		for _, result := range AllRuntimeLookupResults() {
			m.agentRuntimeLookup.WithLabelValues(source, result).Add(0)
		}
	}
	return m
}

// Collectors 返回 PR2 主指标与 PR3 事件指标合并后的完整采集器列表，供一次性注册。
func (m *BusinessMetrics) Collectors() []prometheus.Collector {
	return append([]prometheus.Collector{
		m.taskEnqueued,
		m.taskDispatched,
		m.taskStarted,
		m.taskTerminal,
		m.taskFailed,
		m.taskQueueWait,
		m.taskRunSeconds,
		m.taskTotalSeconds,
		m.taskInProgress,
		m.taskIterations,
		m.taskClaimDecision,
		m.llmTokens,
		m.llmCostUSD,
		m.llmUnpricedTokens,
		m.llmRequests,
		m.taskQueuedExpired,
		m.taskLeaseExpired,
		m.chatClaimSessionFallbackNeeded,
		m.chatClaimSessionFallbackResult,
		m.chatClaimResumeQueryDuration,
		m.runtimeSweepStageDuration,
		m.runtimeSweepCandidateRows,
		m.runtimeSweepRowsChanged,
		m.runtimeGCDeleted,
		m.runtimeGCFailed,
		m.runtimeGCSkipped,
		m.agentRuntimeLookup,
	}, m.events.collectors()...)
}

func (m *BusinessMetrics) RecordClaimDecision(claimPath, outcome, reason string) {
	if m == nil {
		return
	}
	m.taskClaimDecision.WithLabelValues(claimPath, outcome, reason).Inc()
}

// RecordAgentRuntimeLookup 记一次 agent_runtime 单行查询。
//
// 只在 service.RuntimeLookup 里调用、别无分号：该指标的意义就在于每一次读取都被归因，
// 出现第二个入口点，某个调用点就会被数两次或漏数。两个标签都在这里归一化，调用点拼错
// 只会退化成 "other"/"error"，不会凭空造出新的指标序列。
func (m *BusinessMetrics) RecordAgentRuntimeLookup(source, result string) {
	if m == nil {
		return
	}
	m.agentRuntimeLookup.WithLabelValues(
		NormalizeAgentRuntimeLookupSource(source),
		NormalizeAgentRuntimeLookupResult(result),
	).Inc()
}

// RecordRuntimeGCDeleted 记一次 runtime 垃圾回收安全删除了一个过期的离线 runtime。
func (m *BusinessMetrics) RecordRuntimeGCDeleted() {
	if m == nil {
		return
	}
	m.runtimeGCDeleted.Inc()
}

// RecordRuntimeGCFailed 记一次失败的 runtime 垃圾回收操作。
func (m *BusinessMetrics) RecordRuntimeGCFailed() {
	if m == nil {
		return
	}
	m.runtimeGCFailed.Inc()
}

// RecordRuntimeGCSkipped 按原因记一次被安全跳过的 GC 候选。原因经
// normalizeRuntimeGCSkipReason 收进白名单。
func (m *BusinessMetrics) RecordRuntimeGCSkipped(reason string) {
	if m == nil {
		return
	}
	m.runtimeGCSkipped.WithLabelValues(normalizeRuntimeGCSkipReason(reason)).Inc()
}

// normalizeRuntimeGCSkipReason 把 GC 跳过原因收进白名单，不认识归 "unknown"。
func normalizeRuntimeGCSkipReason(reason string) string {
	switch reason {
	case RuntimeGCSkipEligibilityChanged, RuntimeGCSkipNonTerminalTask, RuntimeGCSkipWorkspaceMismatch:
		return reason
	default:
		return "unknown"
	}
}

// ObserveRuntimeSweepStage 记录一次有界基数的维护清扫阶段：耗时 + 候选行数 + 变更行数。
// candidates 是「交回给应用或应用检查过的行数」，不是 PostgreSQL 执行器处理的行数；
// changed 是其中「持久化维护状态真正发生变化」的行数。两者语义不同，分开上报。
func (m *BusinessMetrics) ObserveRuntimeSweepStage(stage string, duration time.Duration, candidates, changed int) {
	if m == nil {
		return
	}
	stage = normalizeRuntimeSweepStage(stage)
	// 防御负值：行数不可能为负，负数说明调用方口径有误，按 0 处理避免指标出现负数。
	if candidates < 0 {
		candidates = 0
	}
	if changed < 0 {
		changed = 0
	}
	m.runtimeSweepStageDuration.WithLabelValues(stage).Observe(duration.Seconds())
	m.runtimeSweepCandidateRows.WithLabelValues(stage).Add(float64(candidates))
	m.runtimeSweepRowsChanged.WithLabelValues(stage).Add(float64(changed))
}

// normalizeRuntimeSweepStage 把清扫阶段名收进白名单，不认识归 "other"。
func normalizeRuntimeSweepStage(stage string) string {
	switch stage {
	case RuntimeSweepStageLiveness,
		RuntimeSweepStageOfflineTasks,
		RuntimeSweepStageReconnectRetries,
		RuntimeSweepStageStaleTasks,
		RuntimeSweepStageQueuedExpiry,
		RuntimeSweepStageDelegatedFailureRecovery,
		RuntimeSweepStageDeferredChatFinalization,
		RuntimeSweepStageGC:
		return stage
	default:
		return "other"
	}
}

// RecordTaskEnqueued 记一个任务入队。source/runtimeMode 就地归一化。
func (m *BusinessMetrics) RecordTaskEnqueued(source, runtimeMode string) {
	if m == nil {
		return
	}
	m.taskEnqueued.WithLabelValues(NormalizeTaskSource(source), NormalizeRuntimeMode(runtimeMode)).Inc()
}

// RecordTaskDispatched 记一个任务分发到 runtime：计数、上报排队等待耗时（非负时），
// 并把任务标记为 in-progress（供终态时精确扣减）。
func (m *BusinessMetrics) RecordTaskDispatched(taskID, source, runtimeMode string, queueWaitSeconds float64) {
	if m == nil {
		return
	}
	source = NormalizeTaskSource(source)
	runtimeMode = NormalizeRuntimeMode(runtimeMode)
	m.taskDispatched.WithLabelValues(source, runtimeMode).Inc()
	if queueWaitSeconds >= 0 {
		m.taskQueueWait.WithLabelValues(source, runtimeMode).Observe(queueWaitSeconds)
	}
	m.markTaskInProgress(taskID, source, runtimeMode)
}

// RecordTaskStarted 记一个任务进入 running 状态（按 provider 拆）。
func (m *BusinessMetrics) RecordTaskStarted(source, runtimeMode, provider string) {
	if m == nil {
		return
	}
	m.taskStarted.WithLabelValues(
		NormalizeTaskSource(source),
		NormalizeRuntimeMode(runtimeMode),
		NormalizeRuntimeProvider(provider),
	).Inc()
}

// RecordTaskTerminal 记一个任务到达终态：终态计数 + 运行/总耗时（非负时）+ 重试轮数，
// 并从 in-progress 表里移除该任务。
func (m *BusinessMetrics) RecordTaskTerminal(taskID, source, runtimeMode, terminalStatus string, runSeconds, totalSeconds float64, attempt int32) {
	if m == nil {
		return
	}
	source = NormalizeTaskSource(source)
	runtimeMode = NormalizeRuntimeMode(runtimeMode)
	terminalStatus = NormalizeTerminalStatus(terminalStatus)
	m.taskTerminal.WithLabelValues(source, runtimeMode, terminalStatus).Inc()
	if runSeconds >= 0 {
		m.taskRunSeconds.WithLabelValues(source, runtimeMode, terminalStatus).Observe(runSeconds)
	}
	if totalSeconds >= 0 {
		m.taskTotalSeconds.WithLabelValues(source, runtimeMode, terminalStatus).Observe(totalSeconds)
	}
	if attempt < 1 {
		attempt = 1
	}
	m.taskIterations.WithLabelValues(source, terminalStatus).Observe(float64(attempt))
	m.clearTaskInProgress(taskID)
}

// RecordTaskFailed 记一个失败的任务（按规范化的失败原因拆）。
func (m *BusinessMetrics) RecordTaskFailed(source, runtimeMode, failureReason string) {
	if m == nil {
		return
	}
	m.taskFailed.WithLabelValues(
		NormalizeTaskSource(source),
		NormalizeRuntimeMode(runtimeMode),
		NormalizeFailureReason(failureReason),
	).Inc()
}

// RecordTaskQueuedExpired 记一个排队任务被调度器判过期。
func (m *BusinessMetrics) RecordTaskQueuedExpired(source, runtimeMode string) {
	if m == nil {
		return
	}
	m.taskQueuedExpired.WithLabelValues(NormalizeTaskSource(source), NormalizeRuntimeMode(runtimeMode)).Inc()
}

// RecordTaskLeaseExpired 记一个已分发/运行中任务的租约被调度器判过期。
func (m *BusinessMetrics) RecordTaskLeaseExpired(source string) {
	if m == nil {
		return
	}
	m.taskLeaseExpired.WithLabelValues(NormalizeTaskSource(source)).Inc()
}

// RecordChatClaimSessionFallbackNeeded 记一次聊天接管（claim）：会话指针里缺
// provider 会话或工作目录，需要走回退查询。
func (m *BusinessMetrics) RecordChatClaimSessionFallbackNeeded() {
	if m == nil {
		return
	}
	m.chatClaimSessionFallbackNeeded.Inc()
}

// RecordChatClaimSessionFallbackHit 记回退查询命中（找到可用会话）。
func (m *BusinessMetrics) RecordChatClaimSessionFallbackHit() {
	m.recordChatClaimSessionFallbackResult("hit")
}

// RecordChatClaimSessionFallbackMiss 记回退查询未命中（确实找不到可用会话）。
func (m *BusinessMetrics) RecordChatClaimSessionFallbackMiss() {
	m.recordChatClaimSessionFallbackResult("miss")
}

// RecordChatClaimSessionFallbackError 记回退查询出错。
func (m *BusinessMetrics) RecordChatClaimSessionFallbackError() {
	m.recordChatClaimSessionFallbackResult("error")
}

// recordChatClaimSessionFallbackResult 回退查询结果计数的共用入口。
func (m *BusinessMetrics) recordChatClaimSessionFallbackResult(result string) {
	if m == nil {
		return
	}
	m.chatClaimSessionFallbackResult.WithLabelValues(result).Inc()
}

// observeChatClaimResumeQuery 记录一次「恢复历史」查询的耗时；耗时非负才计，
// 查询名 query 是固定枚举字符串。
func (m *BusinessMetrics) observeChatClaimResumeQuery(query string, seconds float64) {
	if m == nil || seconds < 0 {
		return
	}
	m.chatClaimResumeQueryDuration.WithLabelValues(query).Observe(seconds)
}

// ObserveChatClaimLastSessionQuery 记录「读最近一次会话」回退查询的耗时。
func (m *BusinessMetrics) ObserveChatClaimLastSessionQuery(seconds float64) {
	m.observeChatClaimResumeQuery("last_session", seconds)
}

// ObserveChatClaimRolloutMissingQuery 记录「会话缺失时按回滚(rollout)查找」回退查询的耗时。
func (m *BusinessMetrics) ObserveChatClaimRolloutMissingQuery(seconds float64) {
	m.observeChatClaimResumeQuery("rollout_missing", seconds)
}

// RecordLLMUsage 记录一轮 LLM 用量与成本。
//
// costUSDTicks 是该 provider 对本次用量自报的价格（单位 1e-10 美元），没报时为 0。
// 只要报了，就以此为准、压过价目表：价目表表达不了请求级规则（例如 xAI 对超过 20 万
// token 的 prompt 加收 2 倍），对这些 provider 本地估算在结构上偏低。
// 价目表命中走 recordPricedTokens 分 token 类型计；没命中走 recordUnpricedTokens。
func (m *BusinessMetrics) RecordLLMUsage(source, runtimeMode, rawProvider, modelAlias string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, costUSDTicks int64) {
	if m == nil {
		return
	}
	source = NormalizeTaskSource(source)
	runtimeMode = NormalizeRuntimeMode(runtimeMode)
	price, priced := PriceForModelAlias(modelAlias)
	if !priced {
		provider := NormalizeRuntimeProvider(rawProvider)
		alias := NormalizeModelAlias(modelAlias)
		m.recordUnpricedTokens(provider, alias, "input", inputTokens)
		m.recordUnpricedTokens(provider, alias, "output", outputTokens)
		m.recordUnpricedTokens(provider, alias, "cache_read", cacheReadTokens)
		m.recordUnpricedTokens(provider, alias, "cache_write", cacheWriteTokens)
		// 价目表缺行不代表没有成本：provider 可能自己给这一轮定了价（`grok-composer-*`
		// 在 Grok Build 目录里，但 xAI 价格表上没有）。只因缺一个其实已不再需要的费率就
		// 丢掉这笔费用，会低估真实开销。没有费率就没有可拆分总额的依据，所以整笔落到
		// `input` 桶——这与 distributeAuthoritativeCost 在没有可缩放形状时用的回退一致。
		if costUSDTicks > 0 {
			m.llmCostUSD.
				WithLabelValues(provider, alias, NormalizeTokenType("input"), runtimeMode, source).
				Add(float64(costUSDTicks) / CostUSDTicksPerUSD)
		}
		m.llmRequests.WithLabelValues(provider, "unknown", runtimeMode).Inc()
		return
	}

	costs := [4]float64{
		tokenCostUSD(inputTokens, price.InputPerM),
		tokenCostUSD(outputTokens, price.OutputPerM),
		tokenCostUSD(cacheReadTokens, price.CacheReadPerM),
		tokenCostUSD(cacheWriteTokens, price.CacheWritePerM),
	}
	if costUSDTicks > 0 {
		costs = distributeAuthoritativeCost(float64(costUSDTicks)/CostUSDTicksPerUSD, costs)
	}

	m.recordPricedTokens(price.Provider, price.Model, "input", runtimeMode, source, inputTokens, costs[0])
	m.recordPricedTokens(price.Provider, price.Model, "output", runtimeMode, source, outputTokens, costs[1])
	m.recordPricedTokens(price.Provider, price.Model, "cache_read", runtimeMode, source, cacheReadTokens, costs[2])
	m.recordPricedTokens(price.Provider, price.Model, "cache_write", runtimeMode, source, cacheWriteTokens, costs[3])
	m.llmRequests.WithLabelValues(price.Provider, price.Model, runtimeMode).Inc()
}

// distributeAuthoritativeCost 把按 token 类型拆的估算成本按比例缩放，使合计等于
// provider 的实际扣费。llm_cost_usd 是按 token_type 拆的，而 provider 给的是整轮
// 一个总价，拆分比例只能自己定——价目表自身的比例是最好的可用猜测，且保证总额精确。
// 只有总额是权威的，按类型拆出的份额仍是估算，所以这里只做缩放、不发明新的标签值。
//
// 当估算总额为零（无费率，或这轮根本没记 token）时没有比例可缩放，整笔费用落到
// `input` 上，避免真实开销从总额里蒸发。
func distributeAuthoritativeCost(actual float64, estimated [4]float64) [4]float64 {
	total := estimated[0] + estimated[1] + estimated[2] + estimated[3]
	if total <= 0 {
		return [4]float64{actual, 0, 0, 0}
	}
	scale := actual / total
	for i := range estimated {
		estimated[i] *= scale
	}
	return estimated
}

// recordPricedTokens 记有单价的一类 token：只在该类型 token 数 > 0 才产生样本，
// 成本 > 0 时一并记入成本计数。
func (m *BusinessMetrics) recordPricedTokens(provider, model, tokenType, runtimeMode, source string, tokens int64, cost float64) {
	tokenType = NormalizeTokenType(tokenType)
	if tokens > 0 {
		m.llmTokens.WithLabelValues(provider, model, tokenType, runtimeMode, source).Add(float64(tokens))
	}
	// Provider-reported cost can exist without a token breakdown.
	if cost > 0 {
		m.llmCostUSD.WithLabelValues(provider, model, tokenType, runtimeMode, source).Add(cost)
	}
}

// recordUnpricedTokens 记「价目表外」模型别名的一类 token（只进 unpriced 计数，不产生
// 成本——没有费率就没有成本可估）。tokens 非正不产生样本。
func (m *BusinessMetrics) recordUnpricedTokens(provider, modelAlias, tokenType string, tokens int64) {
	if tokens <= 0 {
		return
	}
	m.llmUnpricedTokens.WithLabelValues(provider, modelAlias, NormalizeTokenType(tokenType)).Add(float64(tokens))
}

// markTaskInProgress 把任务登记为「进行中」。taskID 为空时（调用方没有任务 ID）无法
// 精确去重/移除，就直接对 in_progress 加一；有 ID 时先查 activeTasks——已经登记过的
// 任务不重复计数（防止同任务重复入队分发把 Gauge 加过头）。
func (m *BusinessMetrics) markTaskInProgress(taskID, source, runtimeMode string) {
	if taskID == "" {
		m.taskInProgress.WithLabelValues(source, runtimeMode).Inc()
		return
	}
	m.activeMu.Lock()
	defer m.activeMu.Unlock()
	if _, ok := m.activeTasks[taskID]; ok {
		return
	}
	m.activeTasks[taskID] = activeTaskLabels{source: source, runtimeMode: runtimeMode}
	m.taskInProgress.WithLabelValues(source, runtimeMode).Inc()
}

// clearTaskInProgress 任务到终态时把它从 activeTasks 移除，并对当初登记时的
// (source, runtimeMode) 标签组合做 -1，保证 Gauge 与真实在途数一致。
// 找不到登记（可能因空 ID 从未登记、或重复终态上报）就静默跳过。
func (m *BusinessMetrics) clearTaskInProgress(taskID string) {
	if taskID == "" {
		return
	}
	m.activeMu.Lock()
	labels, ok := m.activeTasks[taskID]
	if ok {
		delete(m.activeTasks, taskID)
	}
	m.activeMu.Unlock()
	if ok {
		m.taskInProgress.WithLabelValues(labels.source, labels.runtimeMode).Dec()
	}
}

// prewarmFailureReasons 预热 taskFailed 的「来源 × 运行模式 × 全部失败原因」网格，
// 使未发生过的失败组合显示为可见的 0（避免图表上把「没有失败」误读成「没埋点」）。
func (m *BusinessMetrics) prewarmFailureReasons() {
	for _, source := range []string{"issue", "chat", "autopilot", "autopilot_issue", "quick_create", "other"} {
		for _, runtimeMode := range []string{"local", "cloud", "unknown"} {
			for _, reason := range taskfailure.AllReasons() {
				m.taskFailed.WithLabelValues(source, runtimeMode, reason.String()).Add(0)
			}
		}
	}
}

// prewarmClaimDecisions seeds every (claim_path, outcome, reason) series the
// claim paths can emit so an outcome that has not fired since this process
// started reads as zero instead of as a missing series (same rationale as the
// agent_runtime_lookup grid above). The table mirrors claimReasonFor in
// internal/service/task_claim_decision.go; keep the two in sync when an
// outcome is added.
func (m *BusinessMetrics) prewarmClaimDecisions() {
	for _, s := range claimDecisionSeries() {
		m.taskClaimDecision.WithLabelValues(s.claimPath, s.outcome, s.reason).Add(0)
	}
}

type claimDecisionSeriesKey struct{ claimPath, outcome, reason string }

// claimDecisionSeries enumerates the closed set of legal label combinations:
// the claimTask outcomes shared by every poll path, the no_tasks reason split
// by whether the caller had a candidate list, and the poll exits (single
// runtime and batch) that never enter claimTask. Values mirror the constants
// in internal/service/task_claim_decision.go.
func claimDecisionSeries() []claimDecisionSeriesKey {
	claimTaskOutcomes := []claimDecisionSeriesKey{
		{"", "claimed", "claimed"},
		{"", "no_capacity", "agent_concurrency_cap"},
		{"", "no_runtime", "agent_unbound"},
		{"", "runtime_mismatch", "agent_rebound"},
		{"", "error_get_agent", "error"},
		{"", "error_count_running", "error"},
		{"", "error_claim", "error"},
		{"", "error_reanchor_chat_input", "error"},
		{"", "error_transaction", "error"},
	}
	var series []claimDecisionSeriesKey
	for _, claimPath := range []string{"agent", "runtime", "batch"} {
		for _, o := range claimTaskOutcomes {
			series = append(series, claimDecisionSeriesKey{claimPath, o.outcome, o.reason})
		}
	}
	// The agent path has no candidate list, so its no_tasks can only say the
	// cause is unknown; the runtime and batch paths see the candidate count.
	series = append(series, claimDecisionSeriesKey{"agent", "no_tasks", "unknown_candidates"})
	for _, claimPath := range []string{"runtime", "batch"} {
		series = append(series,
			claimDecisionSeriesKey{claimPath, "no_tasks", "candidates_unclaimed"},
			claimDecisionSeriesKey{claimPath, "no_tasks", "no_visible_candidates"},
		)
	}
	for _, s := range []claimDecisionSeriesKey{
		{"runtime", "empty_cache_hit", "empty_cache"},
		{"runtime", "empty_db", "no_visible_candidates"},
		{"runtime", "reclaimed_dispatched", "stale_dispatch_recovered"},
		{"runtime", "error_promote_deferred", "error"},
		{"runtime", "error_reclaim_dispatched", "error"},
		{"runtime", "error_list", "error"},
		{"batch", "empty_cache_hit", "empty_cache"},
		{"batch", "empty_db", "no_visible_candidates"},
		{"batch", "reclaimed_dispatched", "stale_dispatch_recovered"},
		{"batch", "error_promote_deferred", "error"},
		{"batch", "error_reclaim_dispatched", "error"},
		{"batch", "error_list", "error"},
	} {
		series = append(series, s)
	}
	return series
}
