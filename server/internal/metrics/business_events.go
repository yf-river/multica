// 本文件承载「PR3 产品指标」：注册/引导/建仓/autopilot/集成等漏斗、社区、商业事件的
// Prometheus 计数器。这些事件与 PostHog 行为分析事件一一对应、共用同一入口
// RecordEvent，因此两条管道不会走样。任务生命周期/LLM 计费那批指标在 business.go。
package metrics

import (
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/prometheus/client_golang/prometheus"
)

// PR3：漏斗/商业/社区计数器，与 PostHog 事件成对出现。
//
// 每个 PostHog Capture(...) 调用点都必须经过 metrics.RecordEvent(...)（见
// event_recorder.go），保证两侧口径不会漂移；business_pairing_test.go 的 lint 测试
// 强制执行这一约束。

// runtimeReadyBuckets 覆盖冷启动到就绪的耗时，从 <1s 到约 5 分钟。
// 大多数 provider 启动落在 5–60 秒；长尾用于捕获卡住的拉取/启动。
var runtimeReadyBuckets = []float64{1, 2.5, 5, 10, 30, 60, 120, 300, 600}

// cloudRuntimeRequestBuckets 覆盖对外 Fleet/Gateway 调用耗时：从亚 100ms
// （状态探活 ping）到约 30s（provision）。与 cloudruntime.defaultTimeout 对齐。
var cloudRuntimeRequestBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30}

// prMergeSecondsBuckets 覆盖 PR 从打开到合并的延迟：从几分钟到几周。
var prMergeSecondsBuckets = []float64{
	300, 900, 1800,
	3600, 2 * 3600, 6 * 3600, 12 * 3600,
	24 * 3600, 2 * 24 * 3600, 7 * 24 * 3600, 30 * 24 * 3600,
}

// businessEventMetrics 装 PR3 批指标对应的采集器。单独拆一个 struct 是为了让
// business.go（PR2 的任务生命周期 / LLM 指标）保持聚焦；两边都挂在同一个
// BusinessMetrics 接收者上，由同一个 Collectors() 列表对外暴露。
type businessEventMetrics struct {
	signup                          *prometheus.CounterVec
	workspaceCreated                *prometheus.CounterVec
	teamInviteSent                  *prometheus.CounterVec
	teamInviteAccepted              *prometheus.CounterVec
	onboardingStarted               *prometheus.CounterVec
	onboardingQuestionnaireSubmit   *prometheus.CounterVec
	onboardingCompleted             *prometheus.CounterVec
	cloudWaitlistJoined             *prometheus.CounterVec
	issueCreated                    *prometheus.CounterVec
	chatMessageSent                 *prometheus.CounterVec
	agentCreated                    *prometheus.CounterVec
	squadCreated                    *prometheus.CounterVec
	autopilotCreated                *prometheus.CounterVec
	issueExecuted                   *prometheus.CounterVec
	runtimeRegistered               *prometheus.CounterVec
	runtimeReady                    *prometheus.CounterVec
	runtimeReadySeconds             *prometheus.HistogramVec
	runtimeFailed                   *prometheus.CounterVec
	runtimeOffline                  *prometheus.CounterVec
	daemonWSMessageReceived         *prometheus.CounterVec
	autopilotRunStarted             *prometheus.CounterVec
	autopilotRunTerminal            *prometheus.CounterVec
	autopilotRunSkipped             *prometheus.CounterVec
	webhookDelivery                 *prometheus.CounterVec
	webhookRateLimited              *prometheus.CounterVec
	emailRateLimited                *prometheus.CounterVec
	githubEventReceived             *prometheus.CounterVec
	githubPRReview                  *prometheus.CounterVec
	githubPRMergeSeconds            prometheus.Histogram
	cloudRuntimeRequest             *prometheus.CounterVec
	cloudRuntimeRequestDurationSecs *prometheus.HistogramVec
	contactSalesSubmitted           *prometheus.CounterVec
	chatOutputLocalPath             *prometheus.CounterVec
}

// newBusinessEventMetrics 创建 PR3 全部事件指标。每条指标的标签都取自
// metricLabels（即 labels.go 里 businessMetricLabels 登记表），防止少传/多传标签。
func newBusinessEventMetrics() *businessEventMetrics {
	return &businessEventMetrics{
		signup: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_signup_total",
			Help: "Total user signups (account creations).",
		}, metricLabels("multica_signup_total")),
		workspaceCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_workspace_created_total",
			Help: "Total workspaces created.",
		}, metricLabels("multica_workspace_created_total")),
		teamInviteSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_team_invite_sent_total",
			Help: "Total workspace invitations sent.",
		}, metricLabels("multica_team_invite_sent_total")),
		teamInviteAccepted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_team_invite_accepted_total",
			Help: "Total workspace invitations accepted.",
		}, metricLabels("multica_team_invite_accepted_total")),
		onboardingStarted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_onboarding_started_total",
			Help: "Total onboarding flows started.",
		}, metricLabels("multica_onboarding_started_total")),
		onboardingQuestionnaireSubmit: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_onboarding_questionnaire_submitted_total",
			Help: "Total onboarding questionnaires submitted.",
		}, metricLabels("multica_onboarding_questionnaire_submitted_total")),
		onboardingCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_onboarding_completed_total",
			Help: "Total onboarding flows completed.",
		}, metricLabels("multica_onboarding_completed_total")),
		cloudWaitlistJoined: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_cloud_waitlist_joined_total",
			Help: "Total users that joined the cloud waitlist.",
		}, metricLabels("multica_cloud_waitlist_joined_total")),
		issueCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_issue_created_total",
			Help: "Total issues created (any source).",
		}, metricLabels("multica_issue_created_total")),
		chatMessageSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_chat_message_sent_total",
			Help: "Total user chat messages sent (excludes agent replies).",
		}, metricLabels("multica_chat_message_sent_total")),
		agentCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_agent_created_total",
			Help: "Total agents created.",
		}, metricLabels("multica_agent_created_total")),
		squadCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_squad_created_total",
			Help: "Total squads created.",
		}, metricLabels("multica_squad_created_total")),
		autopilotCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_autopilot_created_total",
			Help: "Total autopilots created.",
		}, metricLabels("multica_autopilot_created_total")),
		issueExecuted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_issue_executed_total",
			Help: "First task completion per issue (per-issue exactly-once activation keystone).",
		}, metricLabels("multica_issue_executed_total")),
		runtimeRegistered: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_runtime_registered_total",
			Help: "Total first-time runtime registrations.",
		}, metricLabels("multica_runtime_registered_total")),
		runtimeReady: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_runtime_ready_total",
			Help: "Total runtimes that reached ready state.",
		}, metricLabels("multica_runtime_ready_total")),
		runtimeReadySeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "multica_runtime_ready_seconds",
			Help:    "Time from runtime registration to ready (seconds).",
			Buckets: runtimeReadyBuckets,
		}, metricLabels("multica_runtime_ready_seconds")),
		runtimeFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_runtime_failed_total",
			Help: "Total runtime failures by canonical reason.",
		}, metricLabels("multica_runtime_failed_total")),
		runtimeOffline: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_runtime_offline_total",
			Help: "Total runtime offline transitions.",
		}, metricLabels("multica_runtime_offline_total")),
		daemonWSMessageReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_daemon_ws_message_received_total",
			Help: "Total daemon WebSocket inbound messages by handler kind.",
		}, metricLabels("multica_daemon_ws_message_received_total")),
		autopilotRunStarted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_autopilot_run_started_total",
			Help: "Total autopilot runs started.",
		}, metricLabels("multica_autopilot_run_started_total")),
		autopilotRunTerminal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_autopilot_run_terminal_total",
			Help: "Total autopilot runs that reached a terminal status.",
		}, metricLabels("multica_autopilot_run_terminal_total")),
		autopilotRunSkipped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_autopilot_run_skipped_total",
			Help: "Total autopilot runs that admission-skipped (concurrency / cooldown / other).",
		}, metricLabels("multica_autopilot_run_skipped_total")),
		webhookDelivery: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_webhook_delivery_total",
			Help: "Total inbound webhook deliveries by provider and outcome.",
		}, metricLabels("multica_webhook_delivery_total")),
		webhookRateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_webhook_rate_limited_total",
			Help: "Total webhook admissions or worker dispatches delayed by a bounded safety gate.",
		}, metricLabels("multica_webhook_rate_limited_total")),
		emailRateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_email_rate_limited_total",
			Help: "Total email-producing actions rejected by a bounded safety gate.",
		}, metricLabels("multica_email_rate_limited_total")),
		githubEventReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_github_event_received_total",
			Help: "Total GitHub webhook events received by event kind and action.",
		}, metricLabels("multica_github_event_received_total")),
		githubPRReview: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_github_pr_review_total",
			Help: "Total GitHub pull request reviews observed by result.",
		}, metricLabels("multica_github_pr_review_total")),
		githubPRMergeSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "multica_github_pr_merge_seconds",
			Help:    "Time from PR opened to merged (seconds).",
			Buckets: prMergeSecondsBuckets,
		}),
		cloudRuntimeRequest: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_cloudruntime_request_total",
			Help: "Total outbound cloud runtime requests by op and status bucket.",
		}, metricLabels("multica_cloudruntime_request_total")),
		cloudRuntimeRequestDurationSecs: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "multica_cloudruntime_request_duration_seconds",
			Help:    "Outbound cloud runtime request duration (seconds).",
			Buckets: cloudRuntimeRequestBuckets,
		}, metricLabels("multica_cloudruntime_request_duration_seconds")),
		contactSalesSubmitted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_contact_sales_submitted_total",
			Help: "Total contact-sales inquiries submitted.",
		}, metricLabels("multica_contact_sales_submitted_total")),
		chatOutputLocalPath: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "multica_chat_output_local_path_total",
			Help: "Total agent chat replies that referenced a runtime-local path, by evidence kind. Observation only — the reply is still delivered.",
		}, metricLabels("multica_chat_output_local_path_total")),
	}
}

// collectors 返回 PR3 采集器列表（接收者 nil 时返回 nil）。
func (e *businessEventMetrics) collectors() []prometheus.Collector {
	if e == nil {
		return nil
	}
	return []prometheus.Collector{
		e.signup,
		e.workspaceCreated,
		e.teamInviteSent,
		e.teamInviteAccepted,
		e.onboardingStarted,
		e.onboardingQuestionnaireSubmit,
		e.onboardingCompleted,
		e.cloudWaitlistJoined,
		e.issueCreated,
		e.chatMessageSent,
		e.agentCreated,
		e.squadCreated,
		e.autopilotCreated,
		e.issueExecuted,
		e.runtimeRegistered,
		e.runtimeReady,
		e.runtimeReadySeconds,
		e.runtimeFailed,
		e.runtimeOffline,
		e.daemonWSMessageReceived,
		e.autopilotRunStarted,
		e.autopilotRunTerminal,
		e.autopilotRunSkipped,
		e.webhookDelivery,
		e.webhookRateLimited,
		e.emailRateLimited,
		e.githubEventReceived,
		e.githubPRReview,
		e.githubPRMergeSeconds,
		e.cloudRuntimeRequest,
		e.cloudRuntimeRequestDurationSecs,
		e.contactSalesSubmitted,
		e.chatOutputLocalPath,
	}
}

// RecordEvent 是服务端记录任何漏斗/社区/商业事件的唯一正门：同时做两件事——凡是仍然
// 要进 PostHog 的事件就调用 client.Capture 入队，并推进对应的 Prometheus 计数器，
// 因此两条管道永远同步。`client = nil`（没配 PostHog）或 `m = nil`（无指标）都可安全
// 传入，两侧都是尽力而为、绝不阻塞请求路径。
//
// 自 MUL-4127 起，每个服务端事件都由 analytics.IsMetricsOnly 标记（所有产品事件加上
// runtime_* / autopilot_run_* 生命周期），所以下面的 client.Capture 对它们全都会跳过
// ——服务端分析数据来自数据库和 Grafana，不再走 PostHog。Capture 路径保留只是为了：
// 将来若出现非 metrics-only 的事件名，仍能照常上报。
//
// lint 测试（business_pairing_test.go）会拒绝任何绕过本函数、直接用 analytics.* 构造
// 事件再调 analytics.Client.Capture(...) 的写法。
func RecordEvent(client analytics.Client, m *BusinessMetrics, ev analytics.Event) {
	if client != nil && !analytics.IsMetricsOnly(ev.Name) {
		client.Capture(ev)
	}
	if m != nil {
		m.IncForEvent(ev)
	}
}

// IncForEvent 把一条 analytics.Event 分发到对应的 Prometheus 计数器。不认识的事件名
// 静默忽略——漏配分发的地方由 business_pairing_test.go 的 lint 测试兜底捕获。
func (m *BusinessMetrics) IncForEvent(ev analytics.Event) {
	if m == nil || m.events == nil {
		return
	}
	switch ev.Name {
	case analytics.EventSignup:
		m.events.signup.WithLabelValues(NormalizeSignupSource(stringProp(ev.Properties, "signup_source"))).Inc()
	case analytics.EventWorkspaceCreated:
		m.events.workspaceCreated.WithLabelValues(NormalizeTaskSource(stringProp(ev.Properties, "source"))).Inc()
	case analytics.EventTeamInviteSent:
		m.events.teamInviteSent.WithLabelValues().Inc()
	case analytics.EventTeamInviteAccepted:
		m.events.teamInviteAccepted.WithLabelValues().Inc()
	case analytics.EventOnboardingStarted:
		m.events.onboardingStarted.WithLabelValues(NormalizePlatform(stringProp(ev.Properties, "platform"))).Inc()
	case analytics.EventOnboardingQuestionnaireSubmit:
		m.events.onboardingQuestionnaireSubmit.WithLabelValues().Inc()
	case analytics.EventOnboardingCompleted:
		m.events.onboardingCompleted.WithLabelValues(NormalizeOnboardingPath(stringProp(ev.Properties, "completion_path"))).Inc()
	case analytics.EventCloudWaitlistJoined:
		m.events.cloudWaitlistJoined.WithLabelValues().Inc()
	case analytics.EventIssueCreated:
		m.events.issueCreated.WithLabelValues(
			NormalizeTaskSource(stringProp(ev.Properties, "source")),
			NormalizePlatform(stringProp(ev.Properties, "platform")),
		).Inc()
	case analytics.EventChatMessageSent:
		m.events.chatMessageSent.WithLabelValues(NormalizePlatform(stringProp(ev.Properties, "platform"))).Inc()
	case analytics.EventAgentCreated:
		m.events.agentCreated.WithLabelValues(
			NormalizeRuntimeMode(stringProp(ev.Properties, "runtime_mode")),
			NormalizeTaskSource(stringProp(ev.Properties, "source")),
		).Inc()
	case analytics.EventSquadCreated:
		m.events.squadCreated.WithLabelValues().Inc()
	case analytics.EventAutopilotCreated:
		m.events.autopilotCreated.WithLabelValues(NormalizeAutopilotCadence(stringProp(ev.Properties, "cadence"))).Inc()
	case analytics.EventIssueExecuted:
		m.events.issueExecuted.WithLabelValues(NormalizeTaskSource(stringProp(ev.Properties, "source"))).Inc()
	case analytics.EventRuntimeRegistered:
		m.events.runtimeRegistered.WithLabelValues(
			NormalizeRuntimeMode(stringProp(ev.Properties, "runtime_mode")),
			NormalizeRuntimeProvider(stringProp(ev.Properties, "provider")),
		).Inc()
	case analytics.EventRuntimeReady:
		runtimeMode := NormalizeRuntimeMode(stringProp(ev.Properties, "runtime_mode"))
		provider := NormalizeRuntimeProvider(stringProp(ev.Properties, "provider"))
		m.events.runtimeReady.WithLabelValues(runtimeMode, provider).Inc()
		if d := int64Prop(ev.Properties, "ready_duration_ms"); d > 0 {
			m.events.runtimeReadySeconds.WithLabelValues(runtimeMode, provider).Observe(float64(d) / 1000.0)
		}
	case analytics.EventRuntimeFailed:
		m.events.runtimeFailed.WithLabelValues(
			NormalizeRuntimeMode(stringProp(ev.Properties, "runtime_mode")),
			NormalizeRuntimeProvider(stringProp(ev.Properties, "provider")),
			NormalizeFailureReason(stringProp(ev.Properties, "failure_reason")),
			boolLabel(boolProp(ev.Properties, "recoverable")),
		).Inc()
	case analytics.EventRuntimeOffline:
		m.events.runtimeOffline.WithLabelValues(
			NormalizeRuntimeMode(stringProp(ev.Properties, "runtime_mode")),
			NormalizeRuntimeProvider(stringProp(ev.Properties, "provider")),
		).Inc()
	case analytics.EventAutopilotRunStarted:
		m.events.autopilotRunStarted.WithLabelValues(
			NormalizeAutopilotCadence(stringProp(ev.Properties, "cadence")),
			NormalizeAutopilotTrigger(stringProp(ev.Properties, "trigger_kind")),
		).Inc()
	case analytics.EventAutopilotRunCompleted:
		m.events.autopilotRunTerminal.WithLabelValues(
			NormalizeAutopilotCadence(stringProp(ev.Properties, "cadence")),
			NormalizeAutopilotTrigger(stringProp(ev.Properties, "trigger_kind")),
			"completed",
		).Inc()
	case analytics.EventAutopilotRunFailed:
		m.events.autopilotRunTerminal.WithLabelValues(
			NormalizeAutopilotCadence(stringProp(ev.Properties, "cadence")),
			NormalizeAutopilotTrigger(stringProp(ev.Properties, "trigger_kind")),
			"failed",
		).Inc()
	case analytics.EventContactSalesSubmitted:
		m.events.contactSalesSubmitted.WithLabelValues(NormalizeContactSalesSource(stringProp(ev.Properties, "form_source"))).Inc()
	default:
		// agent_task_* 生命周期遥测直接经类型化方法 BusinessMetrics.RecordTask* 记入
		// Prometheus（它们携带队列/运行/总耗时，这些 analytics.Event 装不下），所以这里
		// 没有对应的 analytics.Event 可分发。任何别的事件落到这个 default 都意味着漏配了
		// case，lint 测试会让 CI 失败。
	}
}

// ---- 非 PostHog 的 Record* 辅助（类型化入参；没有对应的 analytics.Event 源）----

// RecordAutopilotRunSkipped 记一次 autopilot 准入被跳过，带节奏与原因。
func (m *BusinessMetrics) RecordAutopilotRunSkipped(cadence, reason string) {
	if m == nil || m.events == nil {
		return
	}
	m.events.autopilotRunSkipped.WithLabelValues(
		NormalizeAutopilotCadence(cadence),
		NormalizeAutopilotSkipReason(reason),
	).Inc()
}

// RecordWebhookDelivery 记一条入站 webhook 的投递结果（来源方 + 状态）。
func (m *BusinessMetrics) RecordWebhookDelivery(provider, status string) {
	if m == nil || m.events == nil {
		return
	}
	m.events.webhookDelivery.WithLabelValues(
		NormalizeWebhookProvider(provider),
		NormalizeWebhookDeliveryStatus(status),
	).Inc()
}

// RecordWebhookRateLimited 记一次 webhook 因安全闸门（gate）被限流/推迟。
func (m *BusinessMetrics) RecordWebhookRateLimited(gate string) {
	if m == nil || m.events == nil {
		return
	}
	m.events.webhookRateLimited.WithLabelValues(NormalizeWebhookRateLimitGate(gate)).Inc()
}

// RecordEmailRateLimited 记一次产邮件的动作被限流闸门拒绝（动作 + 闸门维度）。
func (m *BusinessMetrics) RecordEmailRateLimited(action, gate string) {
	if m == nil || m.events == nil {
		return
	}
	m.events.emailRateLimited.WithLabelValues(
		NormalizeEmailRateLimitAction(action),
		NormalizeEmailRateLimitGate(gate),
	).Inc()
}

// RecordGithubEventReceived 记一条 GitHub webhook 事件（事件类型 + 动作）。
func (m *BusinessMetrics) RecordGithubEventReceived(eventKind, action string) {
	if m == nil || m.events == nil {
		return
	}
	m.events.githubEventReceived.WithLabelValues(
		NormalizeGithubEventKind(eventKind),
		NormalizeGithubAction(action),
	).Inc()
}

// RecordGithubPRReview 按结论记一次观察到的 PR review。
func (m *BusinessMetrics) RecordGithubPRReview(result string) {
	if m == nil || m.events == nil {
		return
	}
	m.events.githubPRReview.WithLabelValues(NormalizeGithubPRReviewResult(result)).Inc()
}

// ObserveGithubPRMergeSeconds 记录 PR 从打开到合并的耗时（秒）。
// 非正数（拿不到时间或异常值）直接忽略，不进直方图。
func (m *BusinessMetrics) ObserveGithubPRMergeSeconds(seconds float64) {
	if m == nil || m.events == nil || seconds <= 0 {
		return
	}
	m.events.githubPRMergeSeconds.Observe(seconds)
}

// RecordCloudRuntimeRequest 记一次对外 Fleet/Gateway 调用：按「操作 + 状态桶」计数，
// 并在耗时非负时把耗时写进直方图。
func (m *BusinessMetrics) RecordCloudRuntimeRequest(op, status string, durationSeconds float64) {
	if m == nil || m.events == nil {
		return
	}
	op = NormalizeCloudRuntimeOp(op)
	status = NormalizeCloudRuntimeStatus(status)
	m.events.cloudRuntimeRequest.WithLabelValues(op, status).Inc()
	if durationSeconds >= 0 {
		m.events.cloudRuntimeRequestDurationSecs.WithLabelValues(op).Observe(durationSeconds)
	}
}

// RecordChatOutputLocalPath 按证据类型（"file_url" / "workdir_path"）记一次「agent
// 聊天回复里引用了 runtime 本地路径」的事件。
//
// 只做观测、不做拦截：无论哪种情况回复照常送达。服务端没有 daemon 的文件系统可 stat，
// 无法像 CLI lint 那样判断这些路径真假，所以这个指标只衡量 MUL-4899 的 prompt 契约
// 有没有被遵守，绝不能凭词法猜测去卡投递。标签是封闭枚举，正是为了确保路径或回复正文
// 的任何片段都到不了 Prometheus。
func (m *BusinessMetrics) RecordChatOutputLocalPath(kind string) {
	if m == nil || m.events == nil {
		return
	}
	m.events.chatOutputLocalPath.WithLabelValues(NormalizeChatOutputLocalPathKind(kind)).Inc()
}

// RecordDaemonWSMessageReceived 按处理类型记一条入站 daemon WS 消息。
func (m *BusinessMetrics) RecordDaemonWSMessageReceived(kind string) {
	if m == nil || m.events == nil {
		return
	}
	m.events.daemonWSMessageReceived.WithLabelValues(NormalizeDaemonWSKind(kind)).Inc()
}

// ---- 事件属性读取器：从 analytics.Event.Properties 里按 key 安全取值 ----
// 属性 map 来自 JSON 反序列化，实际类型不可控，所以这些函数宁可返回零值也不 panic。
// 其中 boolLabel 是把 bool 变成 "true"/"false" 标签值。

// stringProp 取字符串属性；缺 key / 非字符串 / 值为 nil 都返回空串。
func stringProp(props map[string]any, key string) string {
	if props == nil {
		return ""
	}
	v, ok := props[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// int64Prop 取整数属性；容忍 int32/int/float64 等常见 JSON 反序列化类型，其它返回 0。
func int64Prop(props map[string]any, key string) int64 {
	if props == nil {
		return 0
	}
	v, ok := props[key]
	if !ok || v == nil {
		return 0
	}
	switch x := v.(type) {
	case int64:
		return x
	case int32:
		return int64(x)
	case int:
		return int64(x)
	case float64:
		return int64(x)
	}
	return 0
}

// boolProp 取布尔属性；缺 key 或类型不是 bool 一律返回 false。
func boolProp(props map[string]any, key string) bool {
	if props == nil {
		return false
	}
	v, ok := props[key]
	if !ok || v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// boolLabel 把布尔转成 Prometheus 标签字面量："true"/"false"。
func boolLabel(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
