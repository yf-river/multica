// Package taskfailure owns the failure_reason values persisted on tasks and
// chat messages and exposed as Prometheus labels.
package taskfailure

// Reason is a persisted failure_reason value.
type Reason string

const agentErrorPrefix = "agent_error."

const (
	// Platform, scheduler, runtime, or deliberate workflow outcomes.
	ReasonQueuedExpired           Reason = "queued_expired"
	ReasonRuntimeOffline          Reason = "runtime_offline"
	ReasonRuntimeRecovery         Reason = "runtime_recovery"
	ReasonTimeout                 Reason = "timeout"
	ReasonIterationLimit          Reason = "iteration_limit"
	ReasonAgentFallbackMessage    Reason = "agent_fallback_message"
	ReasonCodexSemanticInactivity Reason = "codex_semantic_inactivity"
	ReasonAgentBlocked            Reason = "agent_blocked"
	ReasonAPIInvalidRequest       Reason = "api_invalid_request"

	// Failures surfaced by an Agent process or its provider.
	ReasonAgentProviderAuthOrAccess        Reason = "agent_error.provider_auth_or_access"
	ReasonAgentProviderQuotaLimit          Reason = "agent_error.provider_quota_limit"
	ReasonAgentProviderCapacityOrRateLimit Reason = "agent_error.provider_capacity_or_rate_limit"
	ReasonAgentProviderServerError         Reason = "agent_error.provider_server_error"
	ReasonAgentProviderNetwork             Reason = "agent_error.provider_network"
	ReasonAgentProcessFailure              Reason = "agent_error.process_failure"
	ReasonAgentEmptyOrUnparseableOutput    Reason = "agent_error.empty_or_unparseable_output"
	ReasonAgentTimeout                     Reason = "agent_error.agent_timeout"
	ReasonAgentContextOverflow             Reason = "agent_error.context_overflow"
	ReasonAgentMissingConfig               Reason = "agent_error.missing_config"
	ReasonAgentModelNotFoundOrUnavailable  Reason = "agent_error.model_not_found_or_unavailable"
	ReasonAgentRuntimeVersionUnsupported   Reason = "agent_error.runtime_version_unsupported"
	ReasonAgentRuntimeMissingExecutable    Reason = "agent_error.runtime_missing_executable"
	ReasonAgentUnknown                     Reason = "agent_error.unknown"
)

// allReasons is stable because metrics pre-warm label series in this order.
var allReasons = []Reason{
	ReasonQueuedExpired,
	ReasonRuntimeOffline,
	ReasonRuntimeRecovery,
	ReasonTimeout,
	ReasonIterationLimit,
	ReasonAgentFallbackMessage,
	ReasonCodexSemanticInactivity,
	ReasonAgentBlocked,
	ReasonAPIInvalidRequest,
	ReasonAgentProviderAuthOrAccess,
	ReasonAgentProviderQuotaLimit,
	ReasonAgentProviderCapacityOrRateLimit,
	ReasonAgentProviderServerError,
	ReasonAgentProviderNetwork,
	ReasonAgentProcessFailure,
	ReasonAgentEmptyOrUnparseableOutput,
	ReasonAgentTimeout,
	ReasonAgentContextOverflow,
	ReasonAgentMissingConfig,
	ReasonAgentModelNotFoundOrUnavailable,
	ReasonAgentRuntimeVersionUnsupported,
	ReasonAgentRuntimeMissingExecutable,
	ReasonAgentUnknown,
}

func (r Reason) String() string { return string(r) }

// AllReasons returns a copy so callers cannot mutate the metric label set.
func AllReasons() []Reason {
	return append([]Reason(nil), allReasons...)
}

// IsResumeUnsafe reports whether the recorded provider session would repeat
// the same terminal failure.
func IsResumeUnsafe(reason string) bool {
	switch Reason(reason) {
	case ReasonIterationLimit,
		ReasonAgentFallbackMessage,
		ReasonAPIInvalidRequest,
		ReasonCodexSemanticInactivity:
		return true
	default:
		return false
	}
}
