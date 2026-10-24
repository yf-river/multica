package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const (
	parentFixture = "/data/ida/artifacts/gong51-plan-execution-20261024/owned-fixture-sessions-private/A-child-parent-exact-source.jsonl"
	childFixture  = "/data/ida/artifacts/gong51-plan-execution-20261024/owned-fixture-sessions-private/A-child-subagent-exact-source.jsonl"
	parentSession = "ce77632e-4824-4b78-820e-1052259487cf"
	childID       = "agent-c4ec9986"
	rootCID       = "14349776a2394785aae272d0d185f663"
)

func stageSubagentTree(t *testing.T, parentBytes, childBytes []byte) string {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "projects", "work")
	sub := filepath.Join(project, parentSession, "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if parentBytes != nil {
		if err := os.WriteFile(filepath.Join(project, parentSession+".jsonl"), parentBytes, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if childBytes != nil {
		if err := os.WriteFile(filepath.Join(sub, childID+".jsonl"), childBytes, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// 真实归档数据：本轮根 CID 的 Agent 工具结果点名的 child，必须被采成 1 条链路。
func TestCodebuddyCollectSubagentUsageRealFixture(t *testing.T) {
	parent, err := os.ReadFile(parentFixture)
	if err != nil {
		t.Skipf("parent fixture unavailable: %v", err)
	}
	child, err := os.ReadFile(childFixture)
	if err != nil {
		t.Skipf("child fixture unavailable: %v", err)
	}
	root := stageSubagentTree(t, parent, child)

	usage, traces := codebuddyCollectSubagentUsage(envOf(root), parentSession, rootCID)
	if len(traces) != 1 {
		t.Fatalf("expected one link, got %d", len(traces))
	}
	if traces[0].TraceID != "7244a07ea37a41618b6649b9c2b5da73" {
		t.Fatalf("cid: %q", traces[0].TraceID)
	}
	if traces[0].SessionID != "9a28459c-30cc-464f-bb93-dbe127550e39" {
		t.Fatalf("session: %q", traces[0].SessionID)
	}
	got := usage["claude-sonnet-5"]
	if got.OutputTokens != 292 || got.CacheWriteTokens != 24950 {
		t.Fatalf("usage: %+v", got)
	}
}

// 红例 1：同一父 session 的下一轮没有任何 Agent 调用，不能把旧 child 再灌进来。
func TestCodebuddyCollectSubagentUsageIgnoresPreviousRound(t *testing.T) {
	parent, err := os.ReadFile(parentFixture)
	if err != nil {
		t.Skipf("parent fixture unavailable: %v", err)
	}
	child, err := os.ReadFile(childFixture)
	if err != nil {
		t.Skipf("child fixture unavailable: %v", err)
	}
	root := stageSubagentTree(t, parent, child)
	// 本轮换了根 CID（比如只跑了一次 Bash），同一个父 session。
	if usage, traces := codebuddyCollectSubagentUsage(envOf(root), parentSession, "ffffffffffffffffffffffffffffffff"); usage != nil || traces != nil {
		t.Fatalf("must not replay an earlier round's child: %v %v", usage, traces)
	}
}

// 红例 2/3：同 key 用量冲突或尾行损坏时整份放弃，不能先加 292 再丢 trace。
func TestCodebuddyCollectSubagentUsageRejectsBrokenChildFile(t *testing.T) {
	parent, err := os.ReadFile(parentFixture)
	if err != nil {
		t.Skipf("parent fixture unavailable: %v", err)
	}
	line := `{"sessionId":"s","providerData":{"conversationRequestId":"c","messageId":"m","traceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","model":"claude-sonnet-5","rawUsage":{"prompt_tokens":100,"completion_tokens":%d}}}`
	cases := map[string]string{
		"conflicting duplicate": line + "\n" + fmt.Sprintf(line, 2) + "\n",
		"malformed tail":        fmt.Sprintf(line, 1) + "\n" + `{"sessionId":"s","providerData":{`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := stageSubagentTree(t, parent, []byte(body))
			if usage, traces := codebuddyCollectSubagentUsage(envOf(root), parentSession, rootCID); usage != nil || traces != nil {
				t.Fatalf("broken child file must yield nothing, got %v %v", usage, traces)
			}
		})
	}
}

// 红例 4：child 文件是 FIFO 时不能阻塞末 Result 的收集。
func TestCodebuddyCollectSubagentUsageSkipsFIFO(t *testing.T) {
	parent, err := os.ReadFile(parentFixture)
	if err != nil {
		t.Skipf("parent fixture unavailable: %v", err)
	}
	root := stageSubagentTree(t, parent, nil)
	sub := filepath.Join(root, "projects", "work", parentSession, "subagents", childID+".jsonl")
	if err := syscall.Mkfifo(sub, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if usage, traces := codebuddyCollectSubagentUsage(envOf(root), parentSession, rootCID); usage != nil || traces != nil {
			t.Errorf("FIFO child must stay unknown: %v %v", usage, traces)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("collector blocked on a FIFO child file")
	}
}

// 只有真正的 Agent/Task 工具结果能带出 child：assistant 正文提到旧 id、Bash 输出里
// 出现同样字样、callId 没有对应 function_call，都不算。
func TestAgentToolChildIDsRequiresRealToolResult(t *testing.T) {
	base := `{"type":"%s","name":"%s","callId":"%s","sessionId":"s","cwd":"/w","providerData":{"conversationRequestId":"` + rootCID + `"},"output":{"type":"text","text":"%s"}}`
	lines := []string{
		// assistant 正文提到旧 id
		`{"type":"assistant","providerData":{"conversationRequestId":"` + rootCID + `"},"message":{"content":[{"type":"text","text":"[Agent ID: agent-aaaaaaaa]"}]}}`,
		// Bash 输出里恰好出现同样字样，且没有对应 function_call
		fmt.Sprintf(base, "function_call_result", "Bash", "call-bash", "[Agent ID: agent-bbbbbbbb]"),
		// Agent 结果但 callId 没有对应的 function_call
		fmt.Sprintf(base, "function_call_result", "Agent", "call-orphan", "[Agent ID: agent-cccccccc]"),
		// 真正成立的：先有 function_call，再有同名同 callId 的结果
		`{"type":"function_call","name":"Agent","callId":"call-real","providerData":{"conversationRequestId":"` + rootCID + `"}}`,
		fmt.Sprintf(base, "function_call_result", "Agent", "call-real", "done [Agent ID: agent-c4ec9986]"),
	}
	body := []byte{}
	for _, line := range lines {
		body = append(body, []byte(line+"\n")...)
	}
	got := agentToolChildIDs(body, rootCID)
	if len(got) != 1 || got[0] != "agent-c4ec9986" {
		t.Fatalf("only the real Agent tool result may contribute a child, got %v", got)
	}
}

// child id 必须是安全文件名，路径穿越/怪字符一律不采。
func TestAgentIDsInTextRejectsUnsafeNames(t *testing.T) {
	cases := []string{
		"[Agent ID: ../../etc/passwd]",
		"[Agent ID: agent-../../x]",
		"[Agent ID: agent-ABC$]",
		"[Agent ID: ]",
	}
	for _, c := range cases {
		if ids := agentIDsInText(c); len(ids) != 0 {
			t.Fatalf("%q must not yield a child id, got %v", c, ids)
		}
	}
	if ids := agentIDsInText("[Agent ID: agent-c4ec9986]"); len(ids) != 1 {
		t.Fatalf("valid id rejected: %v", ids)
	}
}
