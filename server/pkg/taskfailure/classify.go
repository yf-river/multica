package taskfailure

import (
	"regexp"
	"strings"
)

// providerHTTP5xxRe avoids mistaking values such as 1500ms for HTTP status.
var providerHTTP5xxRe = regexp.MustCompile(`(^|[^0-9])5[0-9][0-9]([^0-9]|$)`)

// Classify maps unstructured provider/CLI errors to the persisted failure
// taxonomy. Specific rules precede overlapping generic rules; unmatched or
// empty input is always ReasonAgentUnknown.
func Classify(rawError string) Reason {
	trimmed := strings.TrimSpace(rawError)
	if trimmed == "" {
		return ReasonAgentUnknown
	}
	lower := strings.ToLower(trimmed)

	switch {
	// Context overflow must precede the broader quota rule.
	case containsAny(lower,
		"context length",
		"context_length_exceeded",
		"maximum context",
		"prompt is too long",
		"context size has been exceeded",
	),
		strings.Contains(lower, "token") && strings.Contains(lower, "limit"):
		return ReasonAgentContextOverflow

	// Missing configuration precedes overlapping authentication wording.
	case strings.Contains(lower, "missing environment variable"),
		strings.Contains(lower, "missing") && strings.Contains(lower, "api_key"),
		strings.Contains(lower, "api key") && strings.Contains(lower, "required"),
		strings.Contains(lower, "no llm provider configured"),
		strings.Contains(lower, "no provider configured"):
		return ReasonAgentMissingConfig

	// Authentication and access.
	case containsAny(lower,
		"401",
		"403",
		"unauthorized",
		"login required",
		"not logged in",
		"please login again",
		"refresh token",
		"invalid api key",
		"access token",
		"subscription access",
		"does not have access",
		"you may not have access",
	):
		return ReasonAgentProviderAuthOrAccess

	// Quota and billing.
	case containsAny(lower,
		"402",
		"insufficient_balance",
		"balance is too low",
		"monthly usage limit",
		"usage limit",
		"you've hit your limit",
		"you\u2019ve hit your limit",
		"credits",
		"quota",
	):
		return ReasonAgentProviderQuotaLimit

	// Capacity and rate limits.
	case containsAny(lower,
		"429",
		"rate limit",
		"overloaded",
		"529",
		"no capacity available",
		"at capacity",
	):
		return ReasonAgentProviderCapacityOrRateLimit

	// Provider 5xx and server errors.
	case containsAny(lower,
		"server had an error",
		"provider returned error",
		"internal error",
		"service unavailable",
		"bad gateway",
	),
		providerHTTP5xxRe.MatchString(lower):
		return ReasonAgentProviderServerError

	// Provider network failures below the HTTP response layer.
	case containsAny(lower,
		"stream disconnected",
		"error sending request",
		"unable to connect",
		"dial tcp",
		"connection refused",
		"connectionrefused",
		"dns",
		"i/o timeout",
		"tls handshake eof",
		"failed to connect to websocket",
		"responses_websocket",
		"backend-api/codex/responses",
	):
		return ReasonAgentProviderNetwork

	// Model missing or unavailable.
	case strings.Contains(lower, "model") && strings.Contains(lower, "not found"),
		containsAny(lower,
			"unknown model",
			"selected model",
			"http 404",
			"404 page not found",
		):
		return ReasonAgentModelNotFoundOrUnavailable

	// Stable empty-output errors emitted by Agent backends.
	case containsAny(lower,
		"returned empty output",
		"returned no parseable output",
	):
		return ReasonAgentEmptyOrUnparseableOutput

	// Agent subprocess wall-clock timeout.
	case strings.Contains(lower, "timed out after"):
		return ReasonAgentTimeout

	// Runner executable missing.
	case strings.Contains(lower, "executable not found"):
		return ReasonAgentRuntimeMissingExecutable

	// Runner protocol version unsupported.
	case containsAny(lower,
		"below the minimum supported version",
		"requires a newer version",
	):
		return ReasonAgentRuntimeVersionUnsupported

	// Process failures come last because they often wrap a more specific cause.
	case containsAny(lower,
		"exit status",
		"signal",
		"panic",
		"sigsegv",
		"process exited",
		"pipe has been ended",
		"file already closed",
		"initialize failed",
	):
		return ReasonAgentProcessFailure
	}

	return ReasonAgentUnknown
}

// containsAny expects its input to be lowercased once by the caller.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
