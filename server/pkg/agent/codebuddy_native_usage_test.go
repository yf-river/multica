package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestCodebuddyNativeAccountingPreservesMultipleSDKTracesPerCID(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "projects", "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	row := func(message, trace string, output int) string {
		return fmt.Sprintf(`{"sessionId":"session","providerData":{"conversationRequestId":"goal","messageId":%q,"traceId":%q,"model":"native-flash","rawUsage":{"prompt_tokens":10,"completion_tokens":%d}}}`+"\n", message, trace, output)
	}
	first, second := "11111111111111111111111111111111", "22222222222222222222222222222222"
	raw := row("first", first, 2) + row("second", second, 3) + row("first", second, 2)
	file := filepath.Join(project, "session.jsonl")
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CODEBUDDY_CONFIG_DIR": root}
	usage, traces, calls := codebuddyNativeAccounting(env, "session", map[string]bool{"goal": true})
	if usage["native-flash"].InputTokens != 20 || usage["native-flash"].OutputTokens != 5 || len(traces) != 1 || traces[0].SDKTraceID != "" || traces[0].SDKSourceSHA != "" || len(calls) != 2 {
		t.Fatalf("native Goal multi-trace accounting failed: usage=%+v traces=%+v calls=%+v", usage, traces, calls)
	}
	digest := sha256.Sum256([]byte(raw))
	source := hex.EncodeToString(digest[:])
	if calls[0].SDKTraceID != first || calls[1].SDKTraceID != second || calls[0].SDKSourceSHA != source || calls[1].SDKSourceSHA != source {
		t.Fatal("per-call trace or full-file provenance was lost")
	}
	for _, conflict := range []string{row("first", second, 99), strings.Replace(row("first", first, 2), "native-flash", "different-model", 1)} {
		if err := os.WriteFile(file, []byte(raw+conflict), 0600); err != nil {
			t.Fatal(err)
		}
		usage, traces, calls = codebuddyNativeAccounting(env, "session", map[string]bool{"goal": true})
		if len(usage) != 0 || len(traces) != 0 || len(calls) != 0 {
			t.Fatal("conflicting SDK usage or model was partially billed")
		}
	}
}
