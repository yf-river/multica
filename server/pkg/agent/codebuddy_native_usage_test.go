package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCodebuddyNativeAccountingExcludesHistoryAndDeduplicatesCalls(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "projects", "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	row := func(cid, message, trace string, input, output int) string {
		return fmt.Sprintf(`{"sessionId":"session","providerData":{"conversationRequestId":%q,"messageId":%q,"traceId":%q,"model":"native-flash","rawUsage":{"prompt_tokens":%d,"completion_tokens":%d,"prompt_cache_hit_tokens":2}}}`+"\n", cid, message, trace, input, output)
	}
	raw := row("old", "old-message", "11111111111111111111111111111111", 999, 999) + row("new-1", "message-1", "22222222222222222222222222222222", 10, 3) + row("new-1", "message-1", "22222222222222222222222222222222", 10, 3) + row("new-2", "message-2", "33333333333333333333333333333333", 20, 4)
	if err := os.WriteFile(filepath.Join(project, "session.jsonl"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	usage, traces, calls := codebuddyNativeAccounting(map[string]string{"CODEBUDDY_CONFIG_DIR": root}, "session", map[string]bool{"new-1": true, "new-2": true})
	if usage["native-flash"].InputTokens != 26 || usage["native-flash"].OutputTokens != 7 || len(calls) != 2 || len(traces) != 2 {
		t.Fatalf("usage=%+v traces=%+v calls=%+v", usage, traces, calls)
	}
	digest := sha256.Sum256([]byte(raw))
	source := hex.EncodeToString(digest[:])
	for _, trace := range traces {
		if trace.SDKSourceSHA != source {
			t.Fatal("source hash did not refer to the actual full SDK file")
		}
	}
	usage, _, _ = codebuddyNativeAccounting(map[string]string{"CODEBUDDY_CONFIG_DIR": root}, "session", map[string]bool{"not-observed": true})
	if usage != nil {
		t.Fatal("missing usage became a fabricated zero row")
	}
}
