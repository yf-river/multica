package metrics

import (
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

const (
	labelSource         = "source"
	labelRuntimeMode    = "runtime_mode"
	labelProvider       = "provider"
	labelTerminalStatus = "terminal_status"
	labelFailureReason  = "failure_reason"
	labelTokenType      = "token_type"
	labelModel          = "model"
	labelModelAlias     = "model_alias"

	labelPlatform    = "platform"
	labelCadence     = "cadence"
	labelTriggerKind = "trigger_kind"
	labelReason      = "reason"
	labelRecoverable = "recoverable"
	labelKind        = "kind"
	labelStatus      = "status"
	labelOp          = "op"
)

var businessMetricLabels = map[string][]string{
	"multica_agent_task_enqueued_total":     {labelSource, labelRuntimeMode},
	"multica_agent_task_dispatched_total":   {labelSource, labelRuntimeMode},
	"multica_agent_task_started_total":      {labelSource, labelRuntimeMode, labelProvider},
	"multica_agent_task_terminal_total":     {labelSource, labelRuntimeMode, labelTerminalStatus},
	"multica_agent_task_failed_total":       {labelSource, labelRuntimeMode, labelFailureReason},
	"multica_agent_task_queue_wait_seconds": {labelSource, labelRuntimeMode},
	"multica_agent_task_run_seconds":        {labelSource, labelRuntimeMode, labelTerminalStatus},
	"multica_agent_task_total_seconds":      {labelSource, labelRuntimeMode, labelTerminalStatus},
	"multica_agent_task_in_progress":        {labelSource, labelRuntimeMode},
	"multica_agent_task_iteration_count":    {labelSource, labelTerminalStatus},
	"multica_llm_tokens_total":              {labelProvider, labelModel, labelTokenType, labelRuntimeMode, labelSource},
	"multica_llm_cost_usd_total":            {labelProvider, labelModel, labelTokenType, labelRuntimeMode, labelSource},
	"multica_llm_unpriced_tokens_total":     {labelProvider, labelModelAlias, labelTokenType},
	"multica_llm_request_total":             {labelProvider, labelModel, labelRuntimeMode},
	"multica_task_queued_expired_total":     {labelSource, labelRuntimeMode},
	"multica_task_lease_expired_total":      {labelSource},

	"multica_signup_total":                     {},
	"multica_workspace_created_total":          {labelSource},
	"multica_issue_created_total":              {labelSource, labelPlatform},
	"multica_chat_message_sent_total":          {labelPlatform},
	"multica_agent_created_total":              {labelRuntimeMode, labelSource},
	"multica_squad_created_total":              {},
	"multica_autopilot_created_total":          {labelCadence},
	"multica_issue_executed_total":             {labelSource},
	"multica_runtime_registered_total":         {labelRuntimeMode, labelProvider},
	"multica_runtime_ready_total":              {labelRuntimeMode, labelProvider},
	"multica_runtime_ready_seconds":            {labelRuntimeMode, labelProvider},
	"multica_runtime_failed_total":             {labelRuntimeMode, labelProvider, labelFailureReason, labelRecoverable},
	"multica_runtime_offline_total":            {labelRuntimeMode, labelProvider},
	"multica_daemon_ws_message_received_total": {labelKind},
	"multica_autopilot_run_started_total":      {labelCadence, labelTriggerKind},
	"multica_autopilot_run_terminal_total":     {labelCadence, labelTriggerKind, labelTerminalStatus},
	"multica_autopilot_run_skipped_total":      {labelCadence, labelReason},
	"multica_webhook_delivery_total":           {labelProvider, labelStatus},
	"multica_feedback_submitted_total":         {labelKind, labelPlatform},
}

var forbiddenMetricLabels = map[string]struct{}{
	"workspace_id": {},
	"user_id":      {},
	"agent_id":     {},
	"task_id":      {},
	"issue_id":     {},
	"runtime_id":   {},
	"session_id":   {},
	"ip":           {},
}

var (
	knownSources = map[string]struct{}{
		"issue": {}, "chat": {}, "autopilot": {}, "autopilot_issue": {},
		"quick_create": {}, "manual": {}, "api": {}, "other": {},
	}
	knownRuntimeModes = map[string]struct{}{
		"local": {}, "cloud": {}, "unknown": {},
	}
	knownRuntimeProviders = map[string]struct{}{
		"antigravity": {}, "claude": {}, "codebuddy": {}, "codex": {},
		"copilot": {}, "cursor": {}, "gemini": {}, "hermes": {}, "kiro": {},
		"kimi": {}, "multica_agent": {}, "openclaw": {}, "opencode": {},
		"pi": {}, "other": {},
	}
	knownTerminalStatuses = map[string]struct{}{
		"completed": {}, "failed": {}, "cancelled": {}, "blocked": {}, "other": {},
	}
	knownTokenTypes = map[string]struct{}{
		"input": {}, "output": {}, "cache_read": {}, "cache_write": {},
	}
	knownPlatforms = map[string]struct{}{
		"server": {}, "web": {}, "desktop": {}, "cli": {},
		"mobile": {}, "ios": {}, "unknown": {},
	}
	knownAutopilotCadences = map[string]struct{}{
		"hourly": {}, "daily": {}, "weekly": {}, "monthly": {},
		"manual": {}, "webhook": {}, "unknown": {},
	}
	knownAutopilotTriggers = map[string]struct{}{
		"schedule": {}, "webhook": {}, "manual": {}, "unknown": {},
	}
	knownAutopilotSkipReasons = map[string]struct{}{
		"already_running": {}, "recent_run": {}, "runtime_offline": {},
		"throttled": {}, "max_concurrency": {}, "trigger_disabled": {},
		"signature_invalid": {}, "unknown": {}, "other": {},
	}
	knownWebhookProviders = map[string]struct{}{
		"github": {}, "generic": {}, "gitlab": {}, "stripe": {}, "other": {},
	}
	knownWebhookDeliveryStatuses = map[string]struct{}{
		"queued": {}, "dispatched": {}, "failed": {}, "rejected": {},
		"ignored": {}, "duplicate": {}, "other": {},
	}
	knownDaemonWSKinds = map[string]struct{}{
		"heartbeat": {}, "task_claim": {}, "task_complete": {}, "task_usage": {},
		"task_progress": {}, "task_messages": {}, "log": {}, "other": {},
	}
	knownFeedbackKinds = map[string]struct{}{
		"bug": {}, "feature": {}, "general": {}, "praise": {}, "other": {},
	}
	knownFailureReasons = map[string]struct{}{}
	modelAliasUnsafeRe  = regexp.MustCompile(`[^a-z0-9._:/+-]+`)
)

func init() {
	for _, reason := range taskfailure.AllReasons() {
		knownFailureReasons[reason.String()] = struct{}{}
	}
}

func validateBusinessMetricLabels() {
	for metric, labels := range businessMetricLabels {
		for _, label := range labels {
			if _, forbidden := forbiddenMetricLabels[label]; forbidden {
				panic("forbidden high-cardinality label " + label + " on " + metric)
			}
		}
	}
}

func metricLabels(metric string) []string {
	labels, ok := businessMetricLabels[metric]
	if !ok {
		panic("missing business metric label definition for " + metric)
	}
	return labels
}

func normalizeFromAllowList(value string, allowList map[string]struct{}, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := allowList[value]; ok {
		return value
	}
	return fallback
}

func normalizeTaskSource(value string) string {
	return normalizeFromAllowList(value, knownSources, "other")
}

func normalizeRuntimeMode(value string) string {
	return normalizeFromAllowList(value, knownRuntimeModes, "unknown")
}

func normalizeRuntimeProvider(value string) string {
	return normalizeFromAllowList(value, knownRuntimeProviders, "other")
}

func normalizeTerminalStatus(value string) string {
	return normalizeFromAllowList(value, knownTerminalStatuses, "other")
}

func normalizeFailureReason(value string) string {
	value = strings.TrimSpace(value)
	if _, ok := knownFailureReasons[value]; ok {
		return value
	}
	return taskfailure.Classify(value).String()
}

func normalizeTokenType(value string) string {
	return normalizeFromAllowList(value, knownTokenTypes, "input")
}

func normalizeModelAlias(value string) string {
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

func normalizePlatform(value string) string {
	return normalizeFromAllowList(value, knownPlatforms, "unknown")
}

func normalizeAutopilotCadence(value string) string {
	return normalizeFromAllowList(value, knownAutopilotCadences, "unknown")
}

func normalizeAutopilotTrigger(value string) string {
	return normalizeFromAllowList(value, knownAutopilotTriggers, "unknown")
}

func normalizeAutopilotSkipReason(value string) string {
	return normalizeFromAllowList(value, knownAutopilotSkipReasons, "other")
}

func normalizeWebhookProvider(value string) string {
	return normalizeFromAllowList(value, knownWebhookProviders, "other")
}

func normalizeWebhookDeliveryStatus(value string) string {
	return normalizeFromAllowList(value, knownWebhookDeliveryStatuses, "other")
}

func normalizeDaemonWSKind(value string) string {
	return normalizeFromAllowList(value, knownDaemonWSKinds, "other")
}

func normalizeFeedbackKind(value string) string {
	return normalizeFromAllowList(value, knownFeedbackKinds, "other")
}
