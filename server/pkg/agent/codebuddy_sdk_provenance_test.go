package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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

// 溯源是附加信息：源不是普通文件（FIFO/设备）时必须立刻当未知返回，
// 不能阻塞在读取上——这段代码跑在发送核心 Result 之前。
func TestCodebuddySDKProvenanceSkipsNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "projects", "work")
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(full, "sess.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if id, sum, src := codebuddySDKProvenance(dir, "sess", "/work", "cid"); id != "" || sum != "" || src != "" {
			t.Errorf("non-regular file must stay unknown, got %q %q %q", id, sum, src)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("provenance blocked on a FIFO")
	}
}

func TestCodebuddySDKProvenanceSkipsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "projects", "work")
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(full, "sess.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxSDKLogBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if id, _, _ := codebuddySDKProvenance(dir, "sess", "/work", "cid"); id != "" {
		t.Fatal("oversized file must stay unknown")
	}
}

func TestCodebuddySessionTranscriptMissing(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	root := t.TempDir()
	// 没有 projects 目录：我们对磁盘一无所知，不能据此判定"缺失"。
	if codebuddySessionTranscriptMissing(root, "sess") {
		t.Fatal("unknown layout must not be treated as missing")
	}
	if err := os.MkdirAll(filepath.Join(root, "projects", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !codebuddySessionTranscriptMissing(root, "sess") {
		t.Fatal("empty projects tree means the transcript is gone")
	}
	writeSDKSession(t, root, "work", "sess", `{"providerData":{"conversationRequestId":"cid","traceId":"`+traceA+`"}}`+"\n")
	if codebuddySessionTranscriptMissing(root, "sess") {
		t.Fatal("existing transcript must not be reported missing")
	}
	if codebuddySessionTranscriptMissing(root, "") {
		t.Fatal("no session requested means nothing to check")
	}
}

// 子进程继承 daemon 进程环境：cfg.Env 没写、进程环境有 CODEBUDDY_CONFIG_DIR 时，
// CLI 用的就是进程环境里那个目录，判定必须同样。
func TestCodebuddySessionTranscriptMissingInheritsProcessEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEBUDDY_CONFIG_DIR", root)
	if !codebuddySessionTranscriptMissing("", "gone-session") {
		t.Fatal("inherited config dir must be used, otherwise the guard never fires")
	}
	writeSDKSession(t, root, "work", "live-session", `{"providerData":{"conversationRequestId":"cid","traceId":"`+traceA+`"}}`+"\n")
	if codebuddySessionTranscriptMissing("", "live-session") {
		t.Fatal("present transcript must not be reported missing")
	}
}

// 目录可 stat 但不可读时，"没有这个文件"证明不了，必须保持未知。
func TestCodebuddySessionTranscriptUnknownWhenUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permission checks")
	}
	root := t.TempDir()
	project := filepath.Join(root, "projects", "locked")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(project, 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(project, 0o755) })
	if codebuddySessionTranscriptMissing(root, "sess") {
		t.Fatal("unreadable project dir cannot prove absence")
	}
}

// 快速拒绝路径必须像正常收尾一样关闭两条通道，否则消费者会一直等。
func TestCodebuddyExecuteClosesChannelsOnResumeRejection(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEBUDDY_CONFIG_DIR", root)
	backend := &codebuddyBackend{cfg: Config{
		ExecutablePath: "true",
		Env:            map[string]string{"CODEBUDDY_CONFIG_DIR": root},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	session, err := backend.Execute(context.Background(), "hi", ExecOptions{ResumeSessionID: "gone-session"})
	if err != nil {
		t.Fatalf("expected a rejected session, got error %v", err)
	}
	for range session.Messages {
		t.Fatal("messages channel must be closed and empty")
	}
	result, ok := <-session.Result
	if !ok {
		t.Fatal("result must carry the rejection")
	}
	if !result.ResumeRejected || result.Status != "failed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, ok := <-session.Result; ok {
		t.Fatal("result channel must be closed after the single value")
	}
}
