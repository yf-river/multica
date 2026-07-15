package prompteval

import "testing"

func TestTextProjections(t *testing.T) {
	if got := TruncateEvidence("证据完整", 2); got != "证据..." {
		t.Fatalf("TruncateEvidence() = %q", got)
	}
	if got := TruncateEvidence("ok", 2); got != "ok" {
		t.Fatalf("TruncateEvidence() unchanged = %q", got)
	}
}
