package agent

import (
	"encoding/json"
	"testing"
)

func TestCodebuddySDKResponseIdentityDoesNotDoubleCountOrInventTrace(t *testing.T) {
	b := &codebuddyBackend{}
	usage := map[string]TokenUsage{}
	calls := map[codebuddyUsageCall]TokenUsage{}
	ch := make(chan Message, 20)
	msg := codebuddySDKMessage{Message: json.RawMessage(`{"id":"response-1","model":"deepseek-v4.1-flash","usage":{"input_tokens":100,"output_tokens":5,"cache_read_input_tokens":40},"content":[{"type":"text","text":"visible"}]}`)}
	b.handleAssistant(msg, ch, usage, calls)
	b.handleAssistant(msg, ch, usage, calls)
	want := TokenUsage{InputTokens: 60, OutputTokens: 5, CacheReadTokens: 40}
	if got := usage["deepseek-v4.1-flash"]; got != want {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
	visible, telemetry := 0, 0
	for len(ch) > 0 {
		switch (<-ch).Type {
		case MessageText:
			visible++
		case MessageRuntime:
			telemetry++
		}
	}
	if visible != 2 || telemetry != 1 {
		t.Fatalf("visible content or deduplicated telemetry lost: visible=%d telemetry=%d", visible, telemetry)
	}
	if streamIdentityComplete(usage, calls) {
		t.Fatal("SDK response ID cannot prove external request identity")
	}
	msg.ParentToolUseID = "child-tool"
	b.handleAssistant(msg, ch, usage, calls)
	if usage["deepseek-v4.1-flash"] != want {
		t.Fatal("child stream counted as root usage")
	}
	// 真实调用标识仍然优先，响应 ID 相同的不同请求必须分别计费。
	msg.ParentToolUseID = ""
	msg.ProviderRequestID, msg.ProviderMessageID = "request-2", "message-2"
	b.handleAssistant(msg, ch, usage, calls)
	if got := usage["deepseek-v4.1-flash"]; got.InputTokens != 120 || got.OutputTokens != 10 {
		t.Fatalf("distinct call lost: %+v", got)
	}
}
