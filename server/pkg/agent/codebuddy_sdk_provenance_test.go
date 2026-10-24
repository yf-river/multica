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

const (
	traceA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	traceB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// 同一 session 里既有旧执行也有本轮执行时，各轮必须拿到自己的 SDK trace。
func TestCodebuddySDKProvenancePicksCurrentCID(t *testing.T) {
	dir := t.TempDir()
	body := `{"sessionId":"sess","cwd":"/work","providerData":{"conversationRequestId":"old-cid","traceId":"` + traceA + `"}}
{"sessionId":"sess","cwd":"/work","providerData":{"conversationRequestId":"new-cid","traceId":"` + traceB + `"}}
`
	writeSDKSession(t, dir, "work", "sess", body)

	if id, _, _ := codebuddySDKProvenance(dir, "sess", "/work", "old-cid"); id != traceA {
		t.Fatalf("old cid must get its own trace, got %q", id)
	}
	if id, _, _ := codebuddySDKProvenance(dir, "sess", "/work", "new-cid"); id != traceB {
		t.Fatalf("new cid must get its own trace, got %q", id)
	}
}

// 文件里只有旧执行时，本轮 CID 必须留空——不能把上一轮的 trace 挂到这次执行上。
func TestCodebuddySDKProvenanceNeverAttributesOldRun(t *testing.T) {
	dir := t.TempDir()
	body := `{"sessionId":"sess","cwd":"/work","providerData":{"conversationRequestId":"old-cid","traceId":"` + traceA + `"}}` + "\n"
	writeSDKSession(t, dir, "work", "sess", body)

	if id, _, _ := codebuddySDKProvenance(dir, "sess", "/work", "new-cid"); id != "" {
		t.Fatalf("old run attributed to current run: %q", id)
	}
}

func TestCodebuddySDKProvenanceSourceIsMarkerNotPath(t *testing.T) {
	dir := t.TempDir()
	body := `{"sessionId":"sess","providerData":{"conversationRequestId":"cid","traceId":"` + traceA + `"}}` + "\n"
	writeSDKSession(t, dir, "work", "sess", body)

	_, _, source := codebuddySDKProvenance(dir, "sess", "/work", "cid")
	if source != "providerData.traceId" {
		t.Fatalf("API requires the JSON marker, got %q", source)
	}
	if _, _, _, path := codebuddySDKProvenanceWithSource(dir, "sess", "/work", "cid"); path == "" {
		t.Fatal("private evidence path should still be available")
	}
}

func TestCodebuddySDKProvenanceRejectsWeakEvidence(t *testing.T) {
	cases := map[string]string{
		"two traces for current cid": `{"providerData":{"conversationRequestId":"cid","traceId":"` + traceA + `"}}
{"providerData":{"conversationRequestId":"cid","traceId":"` + traceB + `"}}
`,
		"truncated line": `{"providerData":{"conversationRequestId":"cid","traceId":"` + traceA + `"}}
{"providerData":{"conversationReq`,
		"non hex id":       `{"providerData":{"conversationRequestId":"cid","traceId":"ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"}}` + "\n",
		"short id":         `{"providerData":{"conversationRequestId":"cid","traceId":"abc"}}` + "\n",
		"cwd mismatch":     `{"sessionId":"sess","cwd":"/other","providerData":{"conversationRequestId":"cid","traceId":"` + traceA + `"}}` + "\n",
		"session mismatch": `{"sessionId":"other","providerData":{"conversationRequestId":"cid","traceId":"` + traceA + `"}}` + "\n",
	}
	for name, body := range cases {
		dir := t.TempDir()
		writeSDKSession(t, dir, "work", "sess", body)
		if id, sum, src := codebuddySDKProvenance(dir, "sess", "/work", "cid"); id != "" || sum != "" || src != "" {
			t.Fatalf("%s must stay unknown, got %q %q %q", name, id, sum, src)
		}
	}
}

func TestCodebuddySDKProvenanceMissingInputs(t *testing.T) {
	dir := t.TempDir()
	writeSDKSession(t, dir, "work", "sess", `{"providerData":{"conversationRequestId":"cid","traceId":"`+traceA+`"}}`+"\n")
	for _, args := range [][4]string{{"", "sess", "/work", "cid"}, {dir, "", "/work", "cid"}, {dir, "sess", "/work", ""}} {
		if id, _, _ := codebuddySDKProvenance(args[0], args[1], args[2], args[3]); id != "" {
			t.Fatalf("missing input must stay unknown: %v", args)
		}
	}
	if id, _, _ := codebuddySDKProvenance(dir, "nope", "/work", "cid"); id != "" {
		t.Fatal("missing file must stay unknown")
	}
	// 同一个 session 出现在两个工作目录：无法确定文件归属。
	writeSDKSession(t, dir, "work-b", "sess", `{"providerData":{"conversationRequestId":"cid","traceId":"`+traceA+`"}}`+"\n")
	if id, _, _ := codebuddySDKProvenance(dir, "sess", "/work", "cid"); id != "" {
		t.Fatal("ambiguous file must stay unknown")
	}
}
