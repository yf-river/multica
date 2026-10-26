package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCodebuddyResumedChildAccountsOnlyObservedGeneration(t *testing.T) {
	old := string(testChildTranscript(999))
	fresh := strings.ReplaceAll(string(testChildTranscript(7)), testChildCID, "fresh-child-cid")
	root := stageTree(t, testParentTranscript(), []byte(old+fresh))
	usage, traces, calls := codebuddyCollectScopedSubagentUsage(envOf(root), testParentSession, testRootCID, map[string]bool{"fresh-child-cid": true})
	if usage["claude-sonnet-5"].OutputTokens != 7 || len(traces) != 1 || len(calls) != 1 || calls[0].CID != "fresh-child-cid" {
		t.Fatalf("resumed child replayed old accounting: usage=%v traces=%v calls=%v", usage, traces, calls)
	}
	usage, traces, calls = codebuddyCollectScopedSubagentUsage(envOf(root), testParentSession, testRootCID, map[string]bool{"unobserved": true})
	if len(usage) != 0 || len(traces) != 0 || len(calls) != 0 {
		t.Fatal("child history was billed without a current observed generation")
	}
	file := filepath.Join(root, "projects", "work", testParentSession, "subagents", testChildID+".jsonl")
	conflict := strings.Replace(fresh, `"completion_tokens":7`, `"completion_tokens":8`, 1)
	if err := os.WriteFile(file, []byte(old+fresh+conflict), 0600); err != nil {
		t.Fatal(err)
	}
	usage, traces, calls = codebuddyCollectScopedSubagentUsage(envOf(root), testParentSession, testRootCID, map[string]bool{"fresh-child-cid": true})
	if len(usage) != 0 || len(traces) != 0 || len(calls) != 0 {
		t.Fatal("conflicting resumed child call was partially billed")
	}
}

// 仓库内的 fixture 都是脱敏的最小同协议样本：不复制真实 prompt、任务目录或凭据。
const (
	testParentSession = "11111111-2222-3333-4444-555555555555"
	testRootCID       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testChildCID      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testSDKTrace      = "cccccccccccccccccccccccccccccccc"
	testChildSession  = "99999999-8888-7777-6666-555555555555"
	testChildID       = "agent-3cf4114c-b604-4974-8278-8a6549f0b370"
)

func testParentLine(kind, name, callID, cid, text string) string {
	return fmt.Sprintf(`{"type":%q,"name":%q,"callId":%q,"sessionId":"s","cwd":"/w","providerData":{"conversationRequestId":%q},"output":{"type":"text","text":%q}}`,
		kind, name, callID, cid, text)
}

func testParentTranscript(extra ...string) []byte {
	lines := []string{
		testParentLine("function_call", "Agent", "call-real", testRootCID, ""),
		testParentLine("function_call_result", "Agent", "call-real", testRootCID, "done [Agent ID: "+testChildID+"]"),
	}
	lines = append(lines, extra...)
	return []byte(joinLines(lines))
}

func testChildTranscript(outputTokens int) []byte {
	return []byte(fmt.Sprintf(
		`{"sessionId":%q,"cwd":"/w","providerData":{"conversationRequestId":%q,"messageId":"msg-1","traceId":%q,"model":"claude-sonnet-5","rawUsage":{"prompt_tokens":35336,"completion_tokens":%d,"prompt_cache_hit_tokens":0,"prompt_cache_write_tokens":24950}}}`+"\n",
		testChildSession, testChildCID, testSDKTrace, outputTokens))
}

func joinLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}

func stageTree(t *testing.T, parent, child []byte) string {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "projects", "work")
	sub := filepath.Join(project, testParentSession, "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if parent != nil {
		if err := os.WriteFile(filepath.Join(project, testParentSession+".jsonl"), parent, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if child != nil {
		if err := os.WriteFile(filepath.Join(sub, testChildID+".jsonl"), child, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCodebuddyCollectSubagentUsageCollectsThisRun(t *testing.T) {
	root := stageTree(t, testParentTranscript(), testChildTranscript(292))

	usage, traces, _ := codebuddyCollectSubagentUsage(envOf(root), testParentSession, testRootCID)
	if len(traces) != 1 || traces[0].TraceID != testChildCID || traces[0].SDKTraceID != testSDKTrace {
		t.Fatalf("traces: %+v", traces)
	}
	got := usage["claude-sonnet-5"]
	if got.OutputTokens != 292 || got.CacheWriteTokens != 24950 {
		t.Fatalf("usage: %+v", got)
	}
}

// 同一父 session 的下一轮没有 Agent 调用时，绝不能把上一轮的 child 再算进来。
func TestCodebuddyCollectSubagentUsageIgnoresPreviousRound(t *testing.T) {
	root := stageTree(t, testParentTranscript(), testChildTranscript(292))
	if usage, traces, _ := codebuddyCollectSubagentUsage(envOf(root), testParentSession, "dddddddddddddddddddddddddddddddd"); usage != nil || traces != nil {
		t.Fatalf("must not replay an earlier round's child: %v %v", usage, traces)
	}
}

// 矛盾重复与损坏尾行都必须整份拒绝：不能先加 usage 再丢 trace。
func TestCodebuddyCollectSubagentUsageRejectsBrokenChildFile(t *testing.T) {
	second := fmt.Sprintf(
		`{"sessionId":%q,"providerData":{"conversationRequestId":%q,"messageId":"msg-1","traceId":%q,"model":"claude-sonnet-5","rawUsage":{"prompt_tokens":35336,"completion_tokens":999,"prompt_cache_write_tokens":24950}}}`+"\n",
		testChildSession, testChildCID, testSDKTrace)

	cases := map[string][]byte{
		"conflicting duplicate": append(testChildTranscript(292), []byte(second)...),
		"malformed tail":        append(testChildTranscript(292), []byte(`{"sessionId":"x","providerData":{`)...),
		"non-hex sdk id": []byte(fmt.Sprintf(
			`{"sessionId":%q,"providerData":{"conversationRequestId":%q,"messageId":"msg-9","traceId":%q,"model":"claude-sonnet-5","rawUsage":{"prompt_tokens":10,"completion_tokens":1}}}`+"\n",
			testChildSession, testChildCID, "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")),
		"missing message id": []byte(fmt.Sprintf(
			`{"sessionId":%q,"providerData":{"conversationRequestId":%q,"traceId":%q,"model":"claude-sonnet-5","rawUsage":{"prompt_tokens":10,"completion_tokens":1}}}`+"\n",
			testChildSession, testChildCID, testSDKTrace)),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := stageTree(t, testParentTranscript(), body)
			if usage, traces, _ := codebuddyCollectSubagentUsage(envOf(root), testParentSession, testRootCID); usage != nil || traces != nil {
				t.Fatalf("broken child file must yield nothing, got %v %v", usage, traces)
			}
		})
	}
}

// 同一个调用出现在两个 child 文件里，归属说不清，整份放弃。
func TestCodebuddyCollectSubagentUsageRejectsCrossFileDuplicate(t *testing.T) {
	parent := testParentTranscript(
		testParentLine("function_call", "Agent", "call-two", testRootCID, ""),
		testParentLine("function_call_result", "Agent", "call-two", testRootCID, "done [Agent ID: agent-11111111-2222-3333-4444-555555555555]"),
	)
	root := stageTree(t, parent, testChildTranscript(292))
	sub := filepath.Join(root, "projects", "work", testParentSession, "subagents")
	if err := os.WriteFile(filepath.Join(sub, "agent-11111111-2222-3333-4444-555555555555.jsonl"), testChildTranscript(292), 0o600); err != nil {
		t.Fatal(err)
	}
	if usage, traces, _ := codebuddyCollectSubagentUsage(envOf(root), testParentSession, testRootCID); usage != nil || traces != nil {
		t.Fatalf("same call in two child files must be rejected, got %v %v", usage, traces)
	}
}

// child 文件是 FIFO 时不能让末 Result 卡住。
func TestCodebuddyCollectSubagentUsageSkipsFIFO(t *testing.T) {
	root := stageTree(t, testParentTranscript(), nil)
	sub := filepath.Join(root, "projects", "work", testParentSession, "subagents", testChildID+".jsonl")
	if err := syscall.Mkfifo(sub, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if usage, traces, _ := codebuddyCollectSubagentUsage(envOf(root), testParentSession, testRootCID); usage != nil || traces != nil {
			t.Errorf("FIFO child must stay unknown: %v %v", usage, traces)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("collector blocked on a FIFO child file")
	}
}

// 只有真正的 Agent/Task 工具结果能带出 child。
func TestAgentToolChildIDsRequiresRealToolResult(t *testing.T) {
	body := joinLines([]string{
		`{"type":"assistant","providerData":{"conversationRequestId":"` + testRootCID + `"},"message":{"content":[{"type":"text","text":"[Agent ID: agent-aaaaaaaa]"}]}}`,
		testParentLine("function_call_result", "Bash", "call-bash", testRootCID, "[Agent ID: agent-bbbbbbbb]"),
		testParentLine("function_call_result", "Agent", "call-orphan", testRootCID, "[Agent ID: agent-cccccccc]"),
		testParentLine("function_call", "Agent", "call-real", testRootCID, ""),
		testParentLine("function_call_result", "Agent", "call-real", testRootCID, "done [Agent ID: "+testChildID+"]"),
	})
	got := agentToolChildIDs([]byte(body), testRootCID)
	if len(got) != 1 || got[0] != testChildID {
		t.Fatalf("only the real Agent tool result may contribute a child, got %v", got)
	}
}

func TestAgentIDsInTextAcceptsUUIDShape(t *testing.T) {
	// 2.174.0 的 child id 是带连字符的 UUID；这是脱敏后同形态的样例。
	ids := agentIDsInText("[Agent ID: agent-3cf4114c-b604-4974-8278-8a6549f0b370]")
	if len(ids) != 1 || ids[0] != "agent-3cf4114c-b604-4974-8278-8a6549f0b370" {
		t.Fatalf("uuid-shaped child id rejected: %v", ids)
	}
}

func TestAgentIDsInTextRejectsUnsafeNames(t *testing.T) {
	for _, c := range []string{
		"[Agent ID: ../../etc/passwd]",
		"[Agent ID: agent-../../x]",
		"[Agent ID: agent-ABC$]",
		"[Agent ID: ]",
		"[Agent ID: agent-a/b]",
		"[Agent ID: agent--lead]",
		"[Agent ID: agent-trail-]",
		"[Agent ID: agent-a..b]",
		"[Agent ID: agent-a\\b]",
	} {
		if ids := agentIDsInText(c); len(ids) != 0 {
			t.Fatalf("%q must not yield a child id, got %v", c, ids)
		}
	}
	if ids := agentIDsInText("[Agent ID: agent-c4ec9986]"); len(ids) != 1 {
		t.Fatalf("valid id rejected: %v", ids)
	}
}

// 子调用已经进入 stream（同 CID+messageId+model）时不能再加一次用量。
func TestMergeSubagentUsageSkipsObservedCalls(t *testing.T) {
	call := SubagentCall{
		CID: testChildCID, MessageID: "msg-1", Model: "m",
		Usage: TokenUsage{InputTokens: 10, OutputTokens: 5},
	}
	usage := map[string]TokenUsage{"m": {InputTokens: 100, OutputTokens: 20}}
	observed := map[codebuddyUsageCall]TokenUsage{{testChildCID, "msg-1", "m"}: {}}

	got := mergeSubagentUsage(usage, observed, []SubagentCall{call}, true)
	if got["m"].InputTokens != 100 || got["m"].OutputTokens != 20 {
		t.Fatalf("observed call must not be added twice: %+v", got["m"])
	}
}

// 流里没有这条调用时才补增量。
func TestMergeSubagentUsageAddsUnobservedCalls(t *testing.T) {
	call := SubagentCall{
		CID: testChildCID, MessageID: "msg-2", Model: "m",
		Usage: TokenUsage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3},
	}
	usage := map[string]TokenUsage{"m": {InputTokens: 100, OutputTokens: 20}}
	observed := map[codebuddyUsageCall]TokenUsage{{"other", "msg-9", "m"}: {}}

	got := mergeSubagentUsage(usage, observed, []SubagentCall{call}, true)
	if got["m"].InputTokens != 110 || got["m"].OutputTokens != 25 || got["m"].CacheReadTokens != 3 {
		t.Fatalf("unobserved call must be added: %+v", got["m"])
	}
}

// 流完全没有逐调用身份、但有真实 child 调用时，绝不能加增量（这是之前的真红例）。
func TestMergeSubagentUsageWithoutIdentityAddsNothing(t *testing.T) {
	usage := map[string]TokenUsage{"m": {InputTokens: 100}}
	call := SubagentCall{CID: testChildCID, MessageID: "msg-1", Model: "m",
		Usage: TokenUsage{InputTokens: 10}}
	if got := mergeSubagentUsage(usage, nil, []SubagentCall{call}, false); got["m"].InputTokens != 100 {
		t.Fatalf("unknown stream identity must not add: %+v", got["m"])
	}
}

// 流身份"部分"覆盖（有 usage 但没有对应调用集合）同样不能补。
func TestMergeSubagentUsagePartialIdentityAddsNothing(t *testing.T) {
	usage := map[string]TokenUsage{"m": {InputTokens: 100, OutputTokens: 20}}
	// 只认出其中一个调用：总量对不上，说明还有未识别的调用混在 stream 里。
	observed := map[codebuddyUsageCall]TokenUsage{{"other", "msg-9", "m"}: {InputTokens: 60, OutputTokens: 20}}
	call := SubagentCall{CID: testChildCID, MessageID: "msg-1", Model: "m", Usage: TokenUsage{InputTokens: 10}}
	if streamIdentityComplete(usage, observed) {
		t.Fatal("coverage must be judged incomplete when the child key is missing")
	}
	if got := mergeSubagentUsage(usage, observed, []SubagentCall{call}, false); got["m"].InputTokens != 100 {
		t.Fatalf("incomplete identity must not add: %+v", got["m"])
	}
}

// 覆盖率判定的正例：stream 总量恰好等于逐调用之和。
func TestStreamIdentityComplete(t *testing.T) {
	usage := map[string]TokenUsage{"m": {InputTokens: 30, OutputTokens: 5}}
	observed := map[codebuddyUsageCall]TokenUsage{
		{"c1", "m1", "m"}: {InputTokens: 10, OutputTokens: 2},
		{"c2", "m2", "m"}: {InputTokens: 20, OutputTokens: 3},
	}
	if !streamIdentityComplete(usage, observed) {
		t.Fatal("sum of observed calls matching the total means complete")
	}
	usage["m"] = TokenUsage{InputTokens: 31, OutputTokens: 5}
	if streamIdentityComplete(usage, observed) {
		t.Fatal("a leftover token must make coverage incomplete")
	}
}
