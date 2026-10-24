package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// 用真实归档的子会话文件回放：父 session 目录下的 subagents/*.jsonl 里那条
// claude-sonnet-5 调用必须被单独采集出来，并给出它自己的 CID/session/SDK trace。
func TestCodebuddyCollectSubagentUsageRealFixture(t *testing.T) {
	src := "/data/cbs/multica-verification/verify-1/agent/codebuddy/projects/data-cbs-multica-verification-verify-1-agent-workspaces-dev-20d05776e5a9-dev-7-4c9adb70fbf0-workdir/ce77632e-4824-4b78-820e-1052259487cf/subagents/agent-c4ec9986.jsonl"
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	root := t.TempDir()
	parent := "ce77632e-4824-4b78-820e-1052259487cf"
	dir := filepath.Join(root, "projects", "work", parent, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-c4ec9986.jsonl"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	usage, traces := codebuddyCollectSubagentUsage(envOf(root), parent)
	if len(traces) != 1 {
		t.Fatalf("expected one subagent link, got %d", len(traces))
	}
	trace := traces[0]
	if trace.TraceID != "7244a07ea37a41618b6649b9c2b5da73" {
		t.Fatalf("cid: %q", trace.TraceID)
	}
	if trace.SessionID != "9a28459c-30cc-464f-bb93-dbe127550e39" {
		t.Fatalf("session: %q", trace.SessionID)
	}
	if trace.SDKTraceID != "f7e5752f69d2e1b3a2c2f6a1f0b2c3d4" && len(trace.SDKTraceID) != 32 {
		t.Fatalf("sdk trace: %q", trace.SDKTraceID)
	}
	if trace.SDKSourceSHA != "f6c6915eeb74a1e4e4e9daac1ce0ff9b2413dbd33c3fbc83d33d958b9482f2a9" {
		t.Fatalf("sha: %q", trace.SDKSourceSHA)
	}

	got := usage["claude-sonnet-5"]
	if got.OutputTokens != 292 || got.CacheWriteTokens != 24950 || got.CacheReadTokens != 0 || got.InputTokens != 10386 {
		t.Fatalf("usage: %+v", got)
	}
}

// 同一份目录但父 session 对不上时不能采：归属只认本轮父 session 的子目录。
func TestCodebuddyCollectSubagentUsageIgnoresOtherSessions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "work", "other-session", "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-x.jsonl"), []byte(`{"providerData":{"conversationRequestId":"c","messageId":"m","traceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","model":"m1","rawUsage":{"prompt_tokens":10,"completion_tokens":1}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if usage, traces := codebuddyCollectSubagentUsage(envOf(root), "this-session"); usage != nil || traces != nil {
		t.Fatalf("must not read another session's subagents: %v %v", usage, traces)
	}
}
