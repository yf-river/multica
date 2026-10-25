package taskfailure

import "testing"

func TestIdentifierDigitsDoNotClassifyAsHTTP(t *testing.T) {
	for _, text := range []string{"failed at /tmp/ws-c429f4b/worktree", "failed at run-be401dc", "failed in c503bad", "model qwen3-500b exited"} {
		if got := Classify(text); got != ReasonAgentUnknown {
			t.Errorf("%q: got %s", text, got)
		}
	}
	if got := Classify("model qwen3-500b: HTTP 503"); got != ReasonAgentProviderServerError {
		t.Fatal(got)
	}
}
