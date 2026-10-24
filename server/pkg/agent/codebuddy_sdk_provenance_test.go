package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSDKSession(t *testing.T, dir, project, session, body string) string {
	t.Helper()
	full := filepath.Join(dir, "projects", project)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(full, session+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodebuddySDKProvenanceUniqueFileAndTrace(t *testing.T) {
	dir := t.TempDir()
	body := `{"providerData":{"traceId":"0b2285cd81889d6b8cfbfb514303379b","conversationRequestId":"cid-1"}}
{"providerData":{"traceId":"0b2285cd81889d6b8cfbfb514303379b","conversationRequestId":"cid-1"}}
`
	path := writeSDKSession(t, dir, "work-a", "sess-1", body)

	id, sum, got := codebuddySDKProvenance(dir, "sess-1")
	if id != "0b2285cd81889d6b8cfbfb514303379b" {
		t.Fatalf("trace id: %q", id)
	}
	if got != path || sum == "" {
		t.Fatalf("path=%q sum=%q", got, sum)
	}
	// 同一个 session 在第二个工作目录下再出现一份，归属不再唯一。
	writeSDKSession(t, dir, "work-b", "sess-1", body)
	if id, sum, got := codebuddySDKProvenance(dir, "sess-1"); id != "" || sum != "" || got != "" {
		t.Fatalf("ambiguous file must stay unknown: %q %q %q", id, sum, got)
	}
}

func TestCodebuddySDKProvenanceUnknownWhenTraceIdsDisagree(t *testing.T) {
	dir := t.TempDir()
	body := `{"providerData":{"traceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
{"providerData":{"traceId":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
`
	writeSDKSession(t, dir, "work-a", "sess-2", body)
	if id, _, _ := codebuddySDKProvenance(dir, "sess-2"); id != "" {
		t.Fatalf("two trace ids must stay unknown, got %q", id)
	}
}

func TestCodebuddySDKProvenanceMissingInputs(t *testing.T) {
	dir := t.TempDir()
	if id, _, _ := codebuddySDKProvenance("", "sess"); id != "" {
		t.Fatal("empty config dir must stay unknown")
	}
	if id, _, _ := codebuddySDKProvenance(dir, ""); id != "" {
		t.Fatal("empty session must stay unknown")
	}
	if id, _, _ := codebuddySDKProvenance(dir, "nope"); id != "" {
		t.Fatal("missing file must stay unknown")
	}
	// 文件存在但没有任何 providerData.traceId。
	writeSDKSession(t, dir, "work-a", "sess-3", `{"type":"message"}`+"\n")
	if id, _, _ := codebuddySDKProvenance(dir, "sess-3"); id != "" {
		t.Fatal("no trace id must stay unknown")
	}
}
