package daemon

import (
	"strings"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// poisonedOutputMaxLen caps how long an output can be and still be
// classified as a poisoned fallback. Real fallback messages are short,
// one-sentence affairs; a long output that happens to mention a marker
// is almost certainly a real conclusion (e.g. a code-review reply
// quoting these strings, like the one currently quoting them in
// MUL-1630). The cap intentionally errs on the side of NOT classifying
// — a missed poisoned task gets retried by user action, but a
// false-positive turns a successful task into a failure and a system
// comment.
const poisonedOutputMaxLen = 320

// poisonedMarkers maps a substring fingerprint of a known agent fallback
// terminal message to its failure_reason classifier. Match is case-
// insensitive and substring-based; the cap above prevents long outputs
// that quote a marker from being misclassified.
var poisonedMarkers = []struct {
	Substring string
	Reason    string
}{
	{"i reached the iteration limit", string(taskfailure.ReasonIterationLimit)},
	{"put your final update inside the content string", string(taskfailure.ReasonAgentFallbackMessage)},
}

// classifyPoisonedOutput reports whether output matches a known agent
// fallback terminal message and, if so, returns the failure_reason that
// should be persisted on the task row. Long outputs are never
// classified: a real fallback is the agent's only utterance for the
// turn, so anything beyond ~one paragraph is treated as a real result
// even if it contains a marker substring.
func classifyPoisonedOutput(output string) (string, bool) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || len(trimmed) > poisonedOutputMaxLen {
		return "", false
	}
	lowered := strings.ToLower(trimmed)
	for _, m := range poisonedMarkers {
		if strings.Contains(lowered, m.Substring) {
			return m.Reason, true
		}
	}
	return "", false
}

// classifyPoisonedError recognizes rejected request bodies already embedded
// in session history. Both markers are required so transient/provider errors
// remain resumable.
func classifyPoisonedError(errMsg string) (string, bool) {
	if errMsg == "" {
		return "", false
	}
	lowered := strings.ToLower(errMsg)
	// Both markers must be present: "400" alone is too generic (a tool
	// could surface a 400 from anywhere) and "invalid_request_error"
	// alone could in theory appear in non-poisoning contexts. The
	// combination is the canonical Anthropic error shape and indicates
	// the request body — i.e. the conversation history — is the problem.
	if strings.Contains(lowered, "invalid_request_error") && strings.Contains(lowered, "400") {
		return string(taskfailure.ReasonAPIInvalidRequest), true
	}
	return "", false
}

// classifyResumeUnsafeTimeout reports whether a timeout means the recorded
// session should not be resumed. Keep this intentionally provider-specific:
// ordinary daemon/backend timeouts are infrastructure-shaped and should keep
// the resume pointer so retries can continue the in-flight conversation.
func classifyResumeUnsafeTimeout(provider, errMsg string) (string, bool) {
	if strings.ToLower(strings.TrimSpace(provider)) != "codex" || errMsg == "" {
		return "", false
	}
	lowered := strings.ToLower(errMsg)
	if strings.Contains(lowered, strings.ToLower(agent.CodexSemanticInactivityMarker)) ||
		strings.Contains(lowered, strings.ToLower(agent.CodexFirstTurnNoProgressMarker)) {
		return string(taskfailure.ReasonCodexSemanticInactivity), true
	}
	return "", false
}
