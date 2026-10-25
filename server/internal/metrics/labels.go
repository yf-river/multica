// 本文件是业务指标的「标签字典」与「标签卫生」内核：集中登记每一条业务指标允许带哪些
// 标签、明令禁止哪些高基数标签（workspace_id 等）、把千奇百怪的调用方输入（大小写、
// 拼写变体）归一化成有限的枚举值。它保证指标系列数量有上界，杜绝「用户数多少、指标
// 序列就有多少」的爆炸。
package metrics

import (
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

const (
	// 以下是任务生命周期与 LLM 计费类指标共用的一组基础标签名。
	labelSource         = "source"
	labelRuntimeMode    = "runtime_mode"
	labelProvider       = "provider"
	labelTerminalStatus = "terminal_status"
	labelFailureReason  = "failure_reason"
	labelTokenType      = "token_type"
	labelModel          = "model"
	labelModelAlias     = "model_alias"

	// 以下为漏斗（funnel）/社区（community）/商业（commercial）类指标，
	// 即 PR3 那一批产品增长指标用的标签名。
	labelSignupSource = "signup_source"
	labelPlatform     = "platform"
	labelPath         = "path"
	labelCadence      = "cadence"
	labelTriggerKind  = "trigger_kind"
	labelReason       = "reason"
	labelRecoverable  = "recoverable"
	labelKind         = "kind"
	labelStatus       = "status"
	labelEventKind    = "event_kind"
	labelAction       = "action"
	labelResult       = "result"
	labelQuery        = "query"
	labelOp           = "op"
	labelGate         = "gate"
	labelOutcome      = "outcome"
	labelStage        = "stage"
	labelClaimPath    = "claim_path"
)

// businessMetricLabels 是「业务指标 → 允许的标签」的唯一登记表（指标名全量在此）。
// 每个 CounterVec/HistogramVec 创建时都经 metricLabels 从这张表取标签，因此本文件是
// 业务指标体系结构的单点真源。改标签会影响现有监控看板，需谨慎。
var businessMetricLabels = map[string][]string{
	"multica_agent_task_enqueued_total":                {labelSource, labelRuntimeMode},
	"multica_agent_task_dispatched_total":              {labelSource, labelRuntimeMode},
	"multica_agent_task_started_total":                 {labelSource, labelRuntimeMode, labelProvider},
	"multica_agent_task_terminal_total":                {labelSource, labelRuntimeMode, labelTerminalStatus},
	"multica_agent_task_failed_total":                  {labelSource, labelRuntimeMode, labelFailureReason},
	"multica_agent_task_queue_wait_seconds":            {labelSource, labelRuntimeMode},
	"multica_agent_task_claim_decision_total":          {labelClaimPath, labelOutcome, labelReason},
	"multica_agent_task_run_seconds":                   {labelSource, labelRuntimeMode, labelTerminalStatus},
	"multica_agent_task_total_seconds":                 {labelSource, labelRuntimeMode, labelTerminalStatus},
	"multica_agent_task_in_progress":                   {labelSource, labelRuntimeMode},
	"multica_agent_task_iteration_count":               {labelSource, labelTerminalStatus},
	"multica_llm_tokens_total":                         {labelProvider, labelModel, labelTokenType, labelRuntimeMode, labelSource},
	"multica_llm_cost_usd_total":                       {labelProvider, labelModel, labelTokenType, labelRuntimeMode, labelSource},
	"multica_llm_unpriced_tokens_total":                {labelProvider, labelModelAlias, labelTokenType},
	"multica_llm_request_total":                        {labelProvider, labelModel, labelRuntimeMode},
	"multica_task_queued_expired_total":                {labelSource, labelRuntimeMode},
	"multica_task_lease_expired_total":                 {labelSource},
	"multica_chat_claim_session_fallback_needed_total": {},
	"multica_chat_claim_session_fallback_result_total": {labelResult},
	"multica_chat_claim_resume_query_duration_seconds": {labelQuery},
	"multica_runtime_sweeper_stage_duration_seconds":   {labelStage},
	"multica_runtime_sweeper_candidate_rows_total":     {labelStage},
	"multica_runtime_sweeper_rows_changed_total":       {labelStage},
	"multica_agent_runtime_lookup_total":               {labelSource, labelResult},

	// PR3 funnel / community / commercial.
	"multica_signup_total":                             {labelSignupSource},
	"multica_workspace_created_total":                  {labelSource},
	"multica_team_invite_sent_total":                   {},
	"multica_team_invite_accepted_total":               {},
	"multica_onboarding_started_total":                 {labelPlatform},
	"multica_onboarding_questionnaire_submitted_total": {},
	"multica_onboarding_completed_total":               {labelPath},
	"multica_cloud_waitlist_joined_total":              {},
	"multica_issue_created_total":                      {labelSource, labelPlatform},
	"multica_chat_message_sent_total":                  {labelPlatform},
	"multica_agent_created_total":                      {labelRuntimeMode, labelSource},
	"multica_squad_created_total":                      {},
	"multica_autopilot_created_total":                  {labelCadence},
	"multica_issue_executed_total":                     {labelSource},
	"multica_runtime_registered_total":                 {labelRuntimeMode, labelProvider},
	"multica_runtime_ready_total":                      {labelRuntimeMode, labelProvider},
	"multica_runtime_ready_seconds":                    {labelRuntimeMode, labelProvider},
	"multica_runtime_failed_total":                     {labelRuntimeMode, labelProvider, labelFailureReason, labelRecoverable},
	"multica_runtime_offline_total":                    {labelRuntimeMode, labelProvider},
	"multica_runtime_gc_skipped_total":                 {labelReason},
	"multica_daemon_ws_message_received_total":         {labelKind},
	"multica_autopilot_run_started_total":              {labelCadence, labelTriggerKind},
	"multica_autopilot_run_terminal_total":             {labelCadence, labelTriggerKind, labelTerminalStatus},
	"multica_autopilot_run_skipped_total":              {labelCadence, labelReason},
	"multica_webhook_delivery_total":                   {labelProvider, labelStatus},
	"multica_webhook_rate_limited_total":               {labelGate},
	"multica_email_rate_limited_total":                 {labelAction, labelGate},
	"multica_github_event_received_total":              {labelEventKind, labelAction},
	"multica_github_pr_review_total":                   {labelResult},
	"multica_cloudruntime_request_total":               {labelOp, labelStatus},
	"multica_cloudruntime_request_duration_seconds":    {labelOp},
	"multica_contact_sales_submitted_total":            {labelSource},
	"multica_chat_output_local_path_total":             {labelKind},
}

// forbiddenMetricLabels 是明令禁止用作指标标签的高基数字段。把 workspace_id、
// user_id、agent_id 这类「随租户/用户增长」的标识符做成标签，会让每条租户都产生一串
// 指标，把 Prometheus 撑爆，所以一律拒绝。
var forbiddenMetricLabels = map[string]struct{}{
	"workspace_id": {},
	// installation_id 与上面几类是同一性质：每个频道安装产生一个序列，随租户数增长
	// 而非随部署规模增长。而且任何频道指标都天然想挂它——每个适配器调用点都随身带
	// 着一个——正因如此，若此刻不把它列入黑名单，迟早会漏进来（只是时间问题，不是
	// 运气问题）。
	"installation_id": {},
	"user_id":         {},
	"agent_id":        {},
	"task_id":         {},
	"issue_id":        {},
	"runtime_id":      {},
	"session_id":      {},
	"ip":              {},
}

// 各归一化器的「合法取值表」：key/value 相同，只用来做精确匹配查表。
// 前几个表在文件里写死（封闭枚举，保证标签值集合稳定）；failure_reason 与 runtime
// lookup 的两张表则依赖 taskfailure.AllReasons() / AllRuntimeLookup*() 等外部枚举，
// 无法写死，放到 init() 里动态填充。modelAliasUnsafeRe 用来清洗 model alias 字符串
// 里 Prometheus 不允许的字符。
var (
	knownSources = map[string]string{
		"issue":           "issue",
		"chat":            "chat",
		"autopilot":       "autopilot",
		"autopilot_issue": "autopilot_issue",
		"quick_create":    "quick_create",
		"manual":          "manual",
		"api":             "api",
		"other":           "other",
	}
	knownRuntimeModes = map[string]string{
		"local":   "local",
		"cloud":   "cloud",
		"unknown": "unknown",
	}
	knownRuntimeProviders = map[string]string{
		"antigravity":   "antigravity",
		"claude":        "claude",
		"codearts":      "codearts",
		"codebuddy":     "codebuddy",
		"codex":         "codex",
		"copilot":       "copilot",
		"cursor":        "cursor",
		"dsh":           "dsh",
		"gemini":        "gemini",
		"grok":          "grok",
		"hermes":        "hermes",
		"kiro":          "kiro",
		"kimi":          "kimi",
		"reasonix":      "reasonix",
		"dim":           "dim",
		"mcode":         "mcode",
		"zeroclaw":      "zeroclaw",
		"multica_agent": "multica_agent",
		"opencode":      "opencode",
		"deveco":        "deveco",
		"pi":            "pi",
		"qoder":         "qoder",
		"qoderclicn":    "qoderclicn",
		"qwen":          "qwen",
		"traecli":       "traecli",
		"other":         "other",
	}
	knownTerminalStatuses = map[string]string{
		"completed": "completed",
		"failed":    "failed",
		"cancelled": "cancelled",
		"blocked":   "blocked",
		"other":     "other",
	}
	knownTokenTypes = map[string]string{
		"input":       "input",
		"output":      "output",
		"cache_read":  "cache_read",
		"cache_write": "cache_write",
	}
	knownFailureReasons       = map[string]string{}
	knownRuntimeLookupSources = map[string]string{}
	knownRuntimeLookupResults = map[string]string{}
	modelAliasUnsafeRe        = regexp.MustCompile(`[^a-z0-9._:/+-]+`)
)

// init 把「来自其它包、无法在此写死」的两组合法值灌进查表：
//   - knownFailureReasons：任务失败原因的全部取值；
//   - knownRuntimeLookupSources/Results：agent runtime 单行查询的来源与结果枚举。
func init() {
	for _, reason := range taskfailure.AllReasons() {
		knownFailureReasons[reason.String()] = reason.String()
	}
	for _, source := range AllRuntimeLookupSources() {
		knownRuntimeLookupSources[source] = source
	}
	for _, result := range AllRuntimeLookupResults() {
		knownRuntimeLookupResults[result] = result
	}
}

// validateBusinessMetricLabels 启动期自检：遍历业务指标登记表，一旦发现哪条指标带了
// 被禁止的高基数标签就 panic，把配置错误拦在启动时而非暴露在线上。调用点在
// NewBusinessMetrics 的初始化路径里。
func validateBusinessMetricLabels() {
	for metric, labels := range businessMetricLabels {
		for _, label := range labels {
			if _, forbidden := forbiddenMetricLabels[label]; forbidden {
				panic("forbidden high-cardinality label " + label + " on " + metric)
			}
		}
	}
}

// metricLabels 查登记表取某条业务指标的标签列表。查不到（指标未登记）直接 panic——
// 宁可让开发者立刻发现漏登记，也不放一个没标签的指标悄悄上线。
func metricLabels(metric string) []string {
	labels, ok := businessMetricLabels[metric]
	if !ok {
		panic("missing business metric label definition for " + metric)
	}
	return labels
}

// 以下是 agent_runtime 单行查询（MUL-6884）的来源标签。十来条互不相干的产品路径都会
// 发起对 agent_runtime 表的单行读取，而它们经过 SQL 归一化后在 pg_stat_statements 里
// 是同一条 SQL 指纹——数据库只能告诉你「读了多少次」，永远说不清「是哪个功能来读的」。
// 这些来源标签就是补上缺失的那一半信息。
//
// 这个清单是刻意封闭的枚举：它是系统里最热的那条读路径上的计数器标签，必须无论
// runtime/workspace/路由有多少都保持有界。任何识别不了的值落到 RuntimeLookupSourceOther，
// 正常情况下它应≈0——一旦非零，说明有人加了新调用点却没分类。
const (
	// RuntimeLookupSourceHeartbeatWS 是 daemon WebSocket 心跳：每个 runtime 每隔一个
	// HeartbeatInterval 读一次。
	RuntimeLookupSourceHeartbeatWS = "heartbeat_ws"
	// RuntimeLookupSourceHeartbeatHTTP 是 WebSocket 确认迟迟未到时的回退路径
	// POST /api/daemon/heartbeat。
	RuntimeLookupSourceHeartbeatHTTP = "heartbeat_http"
	// RuntimeLookupSourceDaemonAPI 覆盖除心跳外所有 daemon 鉴权端点：WS 升级、
	// 结果上报、注销、任务拉取。
	RuntimeLookupSourceDaemonAPI = "daemon_api"
	// RuntimeLookupSourceRuntimeModelPoll 是浏览器轮询「模型发现」请求
	// （选择器等待期间每 500ms 一次）。
	RuntimeLookupSourceRuntimeModelPoll = "runtime_model_poll"
	// RuntimeLookupSourceRuntimeLocalSkillPoll 是浏览器轮询本地 skill / MCP 发现请求。
	RuntimeLookupSourceRuntimeLocalSkillPoll = "runtime_local_skill_poll"
	// RuntimeLookupSourceRuntimeLocalSkillImportPoll 是浏览器轮询本地 skill 导入
	// （UI 允许同时最多十个）。
	RuntimeLookupSourceRuntimeLocalSkillImportPoll = "runtime_local_skill_import_poll"
	// RuntimeLookupSourceRuntimeUpdatePoll 是浏览器轮询 CLI 升级进度。
	RuntimeLookupSourceRuntimeUpdatePoll = "runtime_update_poll"
	// RuntimeLookupSourceRuntimeAPI 是其余一切 runtime 维度 API 调用：读取、管理，
	// 以及读权限门禁本身。
	RuntimeLookupSourceRuntimeAPI = "runtime_api"
	// RuntimeLookupSourceIssue 覆盖 issue 的创建/指派/快捷创建，以及触发预览的就绪检查。
	RuntimeLookupSourceIssue = "issue"
	// RuntimeLookupSourceComment 覆盖评论里的 @提及与子 issue 就绪检查。
	RuntimeLookupSourceComment = "comment"
	// RuntimeLookupSourceChat 覆盖聊天发送前的就绪检查。
	RuntimeLookupSourceChat = "chat"
	// RuntimeLookupSourceAutopilot 覆盖 autopilot 的准入与分发。
	RuntimeLookupSourceAutopilot = "autopilot"
	// RuntimeLookupSourceSourceContext 覆盖源码上下文快捷创建的能力开关检查，
	// 包括刻意加的那次「复制后复查」。
	RuntimeLookupSourceSourceContext = "source_context"
	// RuntimeLookupSourceTask 覆盖任务分析上下文与用量 provider 回填。
	RuntimeLookupSourceTask = "task"
	// RuntimeLookupSourceOther 是未分类调用点的兜底。
	RuntimeLookupSourceOther = "other"
)

// 以下是 agent_runtime 查询的结果标签。刻意跟通用状态标签分开，因为这里要区分的
// 关键是「行不见了」（daemon 自愈信号）与「数据库挂了」——把这两种揉成一个标签，
// 会让一起正常删除把真实故障给藏起来。
const (
	RuntimeLookupResultOK       = "ok"
	RuntimeLookupResultNotFound = "not_found"
	RuntimeLookupResultError    = "error"
)

// AllRuntimeLookupSources 按稳定顺序列出全部来源标签。用来预热计数器，使尚未触发过的
// 来源显示为可见的 0，而不是干脆缺一条序列。
func AllRuntimeLookupSources() []string {
	return []string{
		RuntimeLookupSourceHeartbeatWS,
		RuntimeLookupSourceHeartbeatHTTP,
		RuntimeLookupSourceDaemonAPI,
		RuntimeLookupSourceRuntimeModelPoll,
		RuntimeLookupSourceRuntimeLocalSkillPoll,
		RuntimeLookupSourceRuntimeLocalSkillImportPoll,
		RuntimeLookupSourceRuntimeUpdatePoll,
		RuntimeLookupSourceRuntimeAPI,
		RuntimeLookupSourceIssue,
		RuntimeLookupSourceComment,
		RuntimeLookupSourceChat,
		RuntimeLookupSourceAutopilot,
		RuntimeLookupSourceSourceContext,
		RuntimeLookupSourceTask,
		RuntimeLookupSourceOther,
	}
}

// AllRuntimeLookupResults 按稳定顺序列出全部结果标签。
func AllRuntimeLookupResults() []string {
	return []string{RuntimeLookupResultOK, RuntimeLookupResultNotFound, RuntimeLookupResultError}
}

// NormalizeAgentRuntimeLookupSource 把调用点的来源字符串映射到上面的封闭枚举。
// 识别不了的值归为 "other"，而不是为它开一条新序列——保证标签基数始终有界。
func NormalizeAgentRuntimeLookupSource(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownRuntimeLookupSources[value]; ok {
		return normalized
	}
	return RuntimeLookupSourceOther
}

// NormalizeAgentRuntimeLookupResult 把一次查询的结果映射到上面的封闭枚举。
// 识别不了的值按 error 处理：一个没人分类的结果，不能当作「读成功了」的证据。
func NormalizeAgentRuntimeLookupResult(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownRuntimeLookupResults[value]; ok {
		return normalized
	}
	return RuntimeLookupResultError
}

// NormalizeTaskSource 把任务来源归一化到封闭枚举；认不出归 "other"。
// 调用方传什么值（issue/chat/api……）以各自业务含义为准，见 knownSources。
func NormalizeTaskSource(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownSources[value]; ok {
		return normalized
	}
	return "other"
}

// NormalizeRuntimeMode 把 runtime 运行模式归一化：local/cloud；认不出归 "unknown"。
func NormalizeRuntimeMode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownRuntimeModes[value]; ok {
		return normalized
	}
	return "unknown"
}

// NormalizeRuntimeProvider 把 runtime 提供商名称归一化；认不出归 "other"。
// 合法集合见 knownRuntimeProviders（antigravity、codex、cursor……，都是客户端代号）。
func NormalizeRuntimeProvider(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownRuntimeProviders[value]; ok {
		return normalized
	}
	return "other"
}

// NormalizeTerminalStatus 把任务终态归一化：completed/failed/cancelled/blocked；
// 认不出归 "other"。
func NormalizeTerminalStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownTerminalStatuses[value]; ok {
		return normalized
	}
	return "other"
}

// NormalizeFailureReason 把失败原因归一化。先用精确匹配查已知原因表；查不到就交给
// taskfailure.Classify 做模式归类（把错误字符串分类成已知原因类别）。注意它不做
// 大小写折叠——错误原因本身区分大小写。
func NormalizeFailureReason(value string) string {
	value = strings.TrimSpace(value)
	if normalized, ok := knownFailureReasons[value]; ok {
		return normalized
	}
	return taskfailure.Classify(value).String()
}

// NormalizeTokenType 把 token 类型归一化：input/output/cache_read/cache_write；
// 认不出按 input 处理（输入 token 是绝大多数调用的默认口径）。
func NormalizeTokenType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownTokenTypes[value]; ok {
		return normalized
	}
	return "input"
}

// NormalizeModelAlias 清洗「模型别名」标签值，让它适合做 Prometheus 标签：转小写、
// 把非法字符替换成下划线、超长截断到 128 字符、空值给 "unknown"。模型别名来自外部
// 厂商/用户配置，不像其它标签有封闭枚举，只能靠清洗限制基数形状（仍可能较高，故只
// 用在不走价目表的两条兜底路径上：unpriced_tokens 计数，以及未定价 provider 把整单
// 成本落进 model 标签时）。
func NormalizeModelAlias(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	value = modelAliasUnsafeRe.ReplaceAllString(value, "_")
	if len(value) > 128 {
		return value[:128]
	}
	return value
}
