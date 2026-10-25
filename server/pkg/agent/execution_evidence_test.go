package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 1. A resumed Codex session exports only this run's visible events and subtracts its old baseline.
func TestExecutionEvidenceCodexResume(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "sessions", "2026", "10", "25")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Minute).UTC()
	before := started.Add(-time.Second).Format(time.RFC3339Nano)
	after := started.Add(time.Second).Format(time.RFC3339Nano)
	path := filepath.Join(root, "rollout-2026-session-owned.jsonl")
	ended := started.Add(10 * time.Second)
	future := started.Add(20 * time.Second).Format(time.RFC3339Nano)
	body := `{"type":"session_meta","payload":{"id":"session-owned"}}` + "\n" +
		`{"timestamp":"` + before + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":10,"cached_input_tokens":40}}}}` + "\n" +
		`{"timestamp":"` + before + `","type":"response_item","payload":{"text":"old-private-text"}}` + "\n" +
		`{"timestamp":"` + after + `","type":"compacted","payload":{"text":"visible compact summary"}}` + "\n" +
		`{"timestamp":"` + after + `","type":"event_msg","payload":{"type":"token_count","info":{"model":"actual-model","total_token_usage":{"input_tokens":150,"output_tokens":20,"cached_input_tokens":60,"reasoning_output_tokens":4}}}}` + "\n"
	body += `{"timestamp":"` + future + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":9999,"output_tokens":999,"cached_input_tokens":999}}}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rollout-other-foreign.jsonl"), []byte("unrelated-invalid-record"), 0600); err != nil {
		t.Fatal(err)
	}
	e := CollectExecutionEvidence("codex", map[string]string{"CODEX_HOME": home}, "session-owned", "/work", "", started, true, ended)
	if e.Usage == nil || e.Usage.InputTokens != 30 || e.Usage.OutputTokens != 10 || e.Usage.CacheReadTokens != 20 {
		t.Fatalf("wrong run delta %+v", e.Usage)
	}
	encoded, _ := json.Marshal(e)
	if strings.Contains(string(encoded), "old-private-text") || !strings.Contains(string(encoded), "visible compact summary") || e.SHA256 == "" {
		t.Fatalf("wrong visible scope %s", encoded)
	}
	if e := CollectExecutionEvidence("codex", map[string]string{"CODEX_HOME": home}, "../foreign", "/work", "", started, true); len(e.Events) != 0 {
		t.Fatal("path injection accepted")
	}
}

// 2. CodeBuddy request identity comes from this CID's exact session, never a neighboring request.
func TestExecutionEvidenceCodeBuddyRequestScope(t *testing.T) {
	dir := t.TempDir()
	sdk := strings.Repeat("a", 32)
	writeSDKSession(t, dir, "project", "session", `{"type":"assistant","sessionId":"session","cwd":"/work","providerData":{"conversationRequestId":"cid","traceId":"`+sdk+`","messageId":"response","model":"actual","rawUsage":{"prompt_tokens":10,"completion_tokens":2}},"message":{"content":"visible"}}`+"\n"+
		`{"type":"assistant","sessionId":"session","cwd":"/work","providerData":{"conversationRequestId":"other","traceId":"`+sdk+`"},"message":{"content":"unrelated-private-text"}}`+"\n")
	e := CollectExecutionEvidence("codebuddy", map[string]string{"CODEBUDDY_CONFIG_DIR": dir}, "session", "/work", "cid", time.Now().Add(-time.Minute), false)
	if len(e.Requests) != 1 || e.Requests[0].Message != "response" || e.SDK != sdk {
		t.Fatalf("identity %+v", e)
	}
	encoded, _ := json.Marshal(e)
	if strings.Contains(string(encoded), "unrelated-private-text") {
		t.Fatal("unrelated CID leaked")
	}
}

// 3. Generic credentials in tool-return strings are masked without changing usage counters or request identities.
func TestExecutionEvidenceStringRedaction(t *testing.T) {
	value := map[string]any{"output": `{"token":"0123456789abcdef0123456789abcdef","input_tokens":100}`, "trace_id": "trace-owned"}
	redactEvidenceValues(value)
	encoded, _ := json.Marshal(value)
	if strings.Contains(string(encoded), "0123456789abcdef") || !strings.Contains(string(encoded), "input_tokens") || value["trace_id"] != "trace-owned" {
		t.Fatalf("redaction %s", encoded)
	}
}
