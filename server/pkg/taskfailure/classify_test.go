package taskfailure

import "testing"

func TestClassifyRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want Reason
	}{
		{"empty", "", ReasonAgentUnknown},
		{"spaces", "   ", ReasonAgentUnknown},
		{"whitespace", "\n\t  \n", ReasonAgentUnknown},

		// 1. Context overflow.
		{"context length exceeded", "Error: context length exceeded for model gpt-4", ReasonAgentContextOverflow},
		{"context_length_exceeded code", `{"error":{"code":"context_length_exceeded"}}`, ReasonAgentContextOverflow},
		{"maximum context", "Maximum context window of 200000 tokens has been exceeded", ReasonAgentContextOverflow},
		{"prompt is too long", "API Error: prompt is too long: 250000 tokens > 200000 maximum", ReasonAgentContextOverflow},
		{"context size has been exceeded", "context size has been exceeded; consider /compact", ReasonAgentContextOverflow},
		{"token limit", "Hit the token limit for this conversation", ReasonAgentContextOverflow},

		// 2. Missing config.
		{"missing env var", "Missing environment variable: `MIFY_API_KEY`.", ReasonAgentMissingConfig},
		{"missing api_key", "Failed to authenticate: missing api_key in config", ReasonAgentMissingConfig},
		{"api key required", "An api key is required to use this provider", ReasonAgentMissingConfig},
		{"no llm provider configured", "no llm provider configured; set OPENAI_API_KEY", ReasonAgentMissingConfig},
		{"no provider configured", "no provider configured for runtime", ReasonAgentMissingConfig},

		// 3. Provider auth / access.
		{"401", "API Error: 401 Unauthorized", ReasonAgentProviderAuthOrAccess},
		{"403", "API Error: 403 Forbidden", ReasonAgentProviderAuthOrAccess},
		{"unauthorized text", "Request unauthorized for this organization", ReasonAgentProviderAuthOrAccess},
		{"login required", "login required: please run /login", ReasonAgentProviderAuthOrAccess},
		{"not logged in", "Not logged in · Please run /login", ReasonAgentProviderAuthOrAccess},
		{"please login again", "Session expired, please login again", ReasonAgentProviderAuthOrAccess},
		{"refresh token", "refresh token has expired", ReasonAgentProviderAuthOrAccess},
		{"invalid api key", "Invalid API key provided", ReasonAgentProviderAuthOrAccess},
		{"access token", "access token has been revoked", ReasonAgentProviderAuthOrAccess},
		{"subscription access", "Your organization has disabled Claude subscription access for Claude Code", ReasonAgentProviderAuthOrAccess},
		{"does not have access", "Your account does not have access to this model", ReasonAgentProviderAuthOrAccess},
		{"may not have access", "you may not have access to claude-3-opus", ReasonAgentProviderAuthOrAccess},

		// 4. Provider quota / billing.
		{"402", "API Error: 402 Payment Required", ReasonAgentProviderQuotaLimit},
		{"insufficient_balance", `{"error":{"code":"insufficient_balance"}}`, ReasonAgentProviderQuotaLimit},
		{"balance is too low", "balance is too low to make this request", ReasonAgentProviderQuotaLimit},
		{"monthly usage limit", "You've hit your org's monthly usage limit", ReasonAgentProviderQuotaLimit},
		{"usage limit", "Account exceeded the daily usage limit", ReasonAgentProviderQuotaLimit},
		{"hit your limit ascii", "you've hit your limit; upgrade to continue", ReasonAgentProviderQuotaLimit},
		{"hit your limit curly", "you\u2019ve hit your limit", ReasonAgentProviderQuotaLimit},
		{"credits", "Your account has 0 credits remaining", ReasonAgentProviderQuotaLimit},
		{"quota", "quota exceeded for project foo", ReasonAgentProviderQuotaLimit},

		// 5. Capacity / rate limit.
		{"429", "API Error: 429 Too Many Requests", ReasonAgentProviderCapacityOrRateLimit},
		{"529", "Server overloaded: HTTP 529", ReasonAgentProviderCapacityOrRateLimit},
		{"rate limit", "rate limit exceeded for tier 3", ReasonAgentProviderCapacityOrRateLimit},
		{"overloaded", "overloaded_error: please retry", ReasonAgentProviderCapacityOrRateLimit},
		{"no capacity available", "no capacity available; try again later", ReasonAgentProviderCapacityOrRateLimit},
		{"selected model at capacity", "Selected model is at capacity. Please try a different model.", ReasonAgentProviderCapacityOrRateLimit},

		// 6. Provider 5xx / server error.
		{"server had an error", "the server had an error processing your request", ReasonAgentProviderServerError},
		{"provider returned error", "provider returned error: malformed response", ReasonAgentProviderServerError},
		{"internal error", "An internal error occurred while serving the request", ReasonAgentProviderServerError},
		{"500 with delimiter", "API Error: 500 Internal Server Error", ReasonAgentProviderServerError},
		{"503 anywhere", "got HTTP 503 from provider", ReasonAgentProviderServerError},
		{"503 with server text", "503 internal server error", ReasonAgentProviderServerError},
		{"503 at start", "503 service degraded", ReasonAgentProviderServerError},
		{"504 at end", "upstream returned 504", ReasonAgentProviderServerError},
		{"service unavailable", "service unavailable, retry later", ReasonAgentProviderServerError},
		{"bad gateway", "Bad Gateway: upstream rejected", ReasonAgentProviderServerError},
		{"5xx at start", "503", ReasonAgentProviderServerError},
		{"5xx surrounded by spaces", " 504 ", ReasonAgentProviderServerError},
		{"5xx delimited", "got 502 from upstream", ReasonAgentProviderServerError},
		{"5xx at end", "upstream returned 599\n", ReasonAgentProviderServerError},
		{"duration is not 5xx", "1500ms latency observed", ReasonAgentUnknown},
		{"version is not 5xx", "version 1.5.0 unsupported", ReasonAgentUnknown},
		{"token count is not 5xx", "5000 tokens generated", ReasonAgentUnknown},
		{"duration suffix is not 5xx", "agent slept for 1500 seconds", ReasonAgentUnknown},

		// 7. Provider network.
		{"stream disconnected", "stream disconnected before completion", ReasonAgentProviderNetwork},
		{"error sending request", "error sending request for url (https://api.example.com/v1)", ReasonAgentProviderNetwork},
		{"unable to connect", "unable to connect to provider", ReasonAgentProviderNetwork},
		{"dial tcp", "dial tcp 1.2.3.4:443: connect: connection refused", ReasonAgentProviderNetwork},
		{"connection refused alone", "connection refused", ReasonAgentProviderNetwork},
		{"connectionrefused single", "ConnectionRefused", ReasonAgentProviderNetwork},
		{"dns", "dns lookup failed", ReasonAgentProviderNetwork},
		{"i/o timeout", "read tcp 1.2.3.4:443: i/o timeout", ReasonAgentProviderNetwork},
		{"codex tls handshake eof", "failed to connect to websocket: IO error: tls handshake eof", ReasonAgentProviderNetwork},
		{"codex responses websocket", "ERROR codex_api::endpoint::responses_websocket: failed to connect to websocket", ReasonAgentProviderNetwork},
		{"codex responses endpoint", "error sending request for url (https://chatgpt.com/backend-api/codex/responses)", ReasonAgentProviderNetwork},

		// 8. Model not found / unavailable.
		{"model not found", "Error: model claude-3-opus-99 not found", ReasonAgentModelNotFoundOrUnavailable},
		{"model not found phrase", "the model was not found in this account", ReasonAgentModelNotFoundOrUnavailable},
		{"unknown model", "unknown model 'foo-1.0'", ReasonAgentModelNotFoundOrUnavailable},
		{"selected model", "the selected model is no longer supported", ReasonAgentModelNotFoundOrUnavailable},
		{"http 404", "HTTP 404: model endpoint not registered", ReasonAgentModelNotFoundOrUnavailable},
		{"404 page not found", "404 page not found", ReasonAgentModelNotFoundOrUnavailable},

		// 9. Empty / unparseable output.
		{"returned empty output", "agent returned empty output", ReasonAgentEmptyOrUnparseableOutput},
		{"returned no parseable output", "kimi returned no parseable output", ReasonAgentEmptyOrUnparseableOutput},

		// 10. Agent timeout.
		{"timed out after", "claude timed out after 2h0m0s", ReasonAgentTimeout},

		// 11. Runtime missing executable.
		{"executable not found", "executable not found in $PATH", ReasonAgentRuntimeMissingExecutable},

		// 12. Runtime version unsupported.
		{"below the minimum supported version", "claude CLI 0.1.0 is below the minimum supported version 0.5.0", ReasonAgentRuntimeVersionUnsupported},
		{"requires a newer version", "this protocol requires a newer version of the runtime", ReasonAgentRuntimeVersionUnsupported},

		// 13. Process failure.
		{"exit status", "agent exit status 137", ReasonAgentProcessFailure},
		{"signal", "agent terminated by signal: killed", ReasonAgentProcessFailure},
		{"panic", "panic: runtime error: invalid memory address", ReasonAgentProcessFailure},
		{"sigsegv", "fatal error: SIGSEGV", ReasonAgentProcessFailure},
		{"process exited", "process exited with status 1", ReasonAgentProcessFailure},
		{"pipe has been ended", "the pipe has been ended", ReasonAgentProcessFailure},
		{"file already closed", "write |1: file already closed", ReasonAgentProcessFailure},
		{"initialize failed", "initialize failed: backend not ready", ReasonAgentProcessFailure},

		// Overlapping markers must follow the production rule order.
		{"token limit beats quota", "you exceeded the token limit", ReasonAgentContextOverflow},
		{"missing api key beats 401", "missing api_key for openai (401 returned downstream)", ReasonAgentMissingConfig},
		{"429 rate limit", "API Error: 429 rate limit reached", ReasonAgentProviderCapacityOrRateLimit},
		{"upstream auth beats process exit", "exit status 1: API Error: 401 Unauthorized", ReasonAgentProviderAuthOrAccess},

		// 14. Catchall.
		{"unrecognized", "the agent gave up for reasons unknown", ReasonAgentUnknown},
		{"random text", "random text", ReasonAgentUnknown},
		{"sentence with no marker", "Hello world.", ReasonAgentUnknown},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.in); got != c.want {
				t.Fatalf("Classify(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
