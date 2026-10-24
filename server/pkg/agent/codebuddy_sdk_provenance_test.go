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

	if id, _, _ := codebuddySDKProvenance(envOf(dir), "sess", "/work", "old-cid"); id != traceA {
		t.Fatalf("old cid must get its own trace, got %q", id)
	}
	if id, _, _ := codebuddySDKProvenance(envOf(dir), "sess", "/work", "new-cid"); id != traceB {
		t.Fatalf("new cid must get its own trace, got %q", id)
	}
}

// 文件里只有旧执行时，本轮 CID 必须留空——不能把上一轮的 trace 挂到这次执行上。
func TestCodebuddySDKProvenanceNeverAttributesOldRun(t *testing.T) {
	dir := t.TempDir()
	body := `{"sessionId":"sess","cwd":"/work","providerData":{"conversationRequestId":"old-cid","traceId":"` + traceA + `"}}` + "\n"
	writeSDKSession(t, dir, "work", "sess", body)

	if id, _, _ := codebuddySDKProvenance(envOf(dir), "sess", "/work", "new-cid"); id != "" {
		t.Fatalf("old run attributed to current run: %q", id)
	}
}

func TestCodebuddySDKProvenanceSourceIsMarkerNotPath(t *testing.T) {
	dir := t.TempDir()
	body := `{"sessionId":"sess","providerData":{"conversationRequestId":"cid","traceId":"` + traceA + `"}}` + "\n"
	writeSDKSession(t, dir, "work", "sess", body)

	_, _, source := codebuddySDKProvenance(envOf(dir), "sess", "/work", "cid")
	if source != "providerData.traceId" {
		t.Fatalf("API requires the JSON marker, got %q", source)
	}
	if _, _, _, path := codebuddySDKProvenanceWithSource(envOf(dir), "sess", "/work", "cid"); path == "" {
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
		if id, sum, src := codebuddySDKProvenance(envOf(dir), "sess", "/work", "cid"); id != "" || sum != "" || src != "" {
			t.Fatalf("%s must stay unknown, got %q %q %q", name, id, sum, src)
		}
	}
}

func TestCodebuddySDKProvenanceMissingInputs(t *testing.T) {
	dir := t.TempDir()
	writeSDKSession(t, dir, "work", "sess", `{"providerData":{"conversationRequestId":"cid","traceId":"`+traceA+`"}}`+"\n")
	for _, args := range [][4]string{{"", "sess", "/work", "cid"}, {dir, "", "/work", "cid"}, {dir, "sess", "/work", ""}} {
		if id, _, _ := codebuddySDKProvenance(envOf(args[0]), args[1], args[2], args[3]); id != "" {
			t.Fatalf("missing input must stay unknown: %v", args)
		}
	}
	if id, _, _ := codebuddySDKProvenance(envOf(dir), "nope", "/work", "cid"); id != "" {
		t.Fatal("missing file must stay unknown")
	}
	// 同一个 session 出现在两个工作目录：无法确定文件归属。
	writeSDKSession(t, dir, "work-b", "sess", `{"providerData":{"conversationRequestId":"cid","traceId":"`+traceA+`"}}`+"\n")
	if id, _, _ := codebuddySDKProvenance(envOf(dir), "sess", "/work", "cid"); id != "" {
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
		if id, sum, src := codebuddySDKProvenance(envOf(dir), "sess", "/work", "cid"); id != "" || sum != "" || src != "" {
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
	if id, _, _ := codebuddySDKProvenance(envOf(dir), "sess", "/work", "cid"); id != "" {
		t.Fatal("oversized file must stay unknown")
	}
}

func TestCodebuddySessionTranscriptMissing(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	root := t.TempDir()
	// 没有 projects 目录：我们对磁盘一无所知，不能据此判定"缺失"。
	if codebuddySessionTranscriptMissing(envOf(root), "sess") {
		t.Fatal("unknown layout must not be treated as missing")
	}
	if err := os.MkdirAll(filepath.Join(root, "projects", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !codebuddySessionTranscriptMissing(envOf(root), "sess") {
		t.Fatal("empty projects tree means the transcript is gone")
	}
	writeSDKSession(t, root, "work", "sess", `{"providerData":{"conversationRequestId":"cid","traceId":"`+traceA+`"}}`+"\n")
	if codebuddySessionTranscriptMissing(envOf(root), "sess") {
		t.Fatal("existing transcript must not be reported missing")
	}
	if codebuddySessionTranscriptMissing(envOf(root), "") {
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
	if !codebuddySessionTranscriptMissing(nil, "gone-session") {
		t.Fatal("inherited config dir must be used, otherwise the guard never fires")
	}
	writeSDKSession(t, root, "work", "live-session", `{"providerData":{"conversationRequestId":"cid","traceId":"`+traceA+`"}}`+"\n")
	if codebuddySessionTranscriptMissing(nil, "live-session") {
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
	if codebuddySessionTranscriptMissing(envOf(root), "sess") {
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

func envOf(dir string) map[string]string {
	if dir == "" {
		return nil
	}
	return map[string]string{"CODEBUDDY_CONFIG_DIR": dir}
}

// 显式空值会用空字符串覆盖进程环境（buildEnv 的语义），此时不能回读进程里的目录，
// 否则会从另一个 projects 树里得出"这个 session 不见了"的错误结论。
func TestCodebuddySessionTranscriptExplicitEmptyOverridesProcessEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEBUDDY_CONFIG_DIR", root)
	env := map[string]string{"CODEBUDDY_CONFIG_DIR": ""}
	if codebuddySessionTranscriptMissing(env, "gone-session") {
		t.Fatal("explicit empty value must stay unknown, not fall back to the process env")
	}
	if id, sum, src := codebuddySDKProvenance(env, "gone-session", "/work", "cid"); id != "" || sum != "" || src != "" {
		t.Fatalf("provenance must stay unknown for an explicitly empty config dir, got %q %q %q", id, sum, src)
	}
}

// 项目条目是符号链接时内容没有核实过，不能据此认定整棵树可读且不存在。
func TestCodebuddySessionTranscriptUnknownOnSymlinkedProject(t *testing.T) {
	root := t.TempDir()
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "inner"), filepath.Join(root, "projects", "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if codebuddySessionTranscriptMissing(envOf(root), "sess") {
		t.Fatal("unverified symlinked project cannot prove absence")
	}
}

// 子进程看到的 HOME 可能被 cfg.Env 覆盖；daemon 自己的 HOME 不能拿来判断子进程的
// 默认配置目录，否则会把真实存在的 session 判成缺失。
func TestResolveSDKConfigDirUsesChildHome(t *testing.T) {
	daemonHome := t.TempDir()
	childHome := t.TempDir()
	t.Setenv("HOME", daemonHome)
	got := resolveSDKConfigDir(map[string]string{"HOME": childHome})
	if got != filepath.Join(childHome, ".codebuddy") {
		t.Fatalf("must follow the child's HOME, got %q", got)
	}
	if got := resolveSDKConfigDir(nil); got != filepath.Join(daemonHome, ".codebuddy") {
		t.Fatalf("no override means the process HOME, got %q", got)
	}
}

// 只有 HOME、没有明确配置目录时，preflight 不能仅凭推导出的默认位置判定缺失。
func TestCodebuddySessionTranscriptUnknownWithoutExplicitConfigDir(t *testing.T) {
	childHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(childHome, ".codebuddy", "projects", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if codebuddySessionTranscriptMissing(map[string]string{"HOME": childHome}, "sess") {
		t.Fatal("a derived default directory cannot prove absence")
	}
}

// 但 provenance 定位必须按子进程 HOME 去真实的树里取，否则元数据会指向错的目录。
func TestCodebuddySDKProvenanceFollowsChildHome(t *testing.T) {
	childHome := t.TempDir()
	root := filepath.Join(childHome, ".codebuddy")
	writeSDKSession(t, root, "work", "sess", `{"sessionId":"sess","providerData":{"conversationRequestId":"cid","traceId":"`+traceA+`"}}`+"\n")
	id, sum, src := codebuddySDKProvenance(map[string]string{"HOME": childHome}, "sess", "/work", "cid")
	if id != traceA || sum == "" || src != "providerData.traceId" {
		t.Fatalf("provenance must read the child's tree, got %q %q %q", id, sum, src)
	}
}
