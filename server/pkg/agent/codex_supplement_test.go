package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSupplementCodexTurnTargetsExactActiveTurn(t *testing.T) {
	c, _, _ := newTestCodexClient(t)
	c.threadID = "thread-current"
	c.setActiveTurnID("turn-current")
	stdin := &fakeStdinWithHook{}
	stdin.afterWrite = func() {
		c.handleLine(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	}
	c.stdin = stdin

	if err := supplementCodexTurn(context.Background(), c, "keep the original goal and add this"); err != nil {
		t.Fatalf("supplementCodexTurn: %v", err)
	}
	lines := stdin.Lines()
	if len(lines) != 1 {
		t.Fatalf("request lines = %d, want 1", len(lines))
	}
	var request struct {
		Method string `json:"method"`
		Params struct {
			ThreadID       string `json:"threadId"`
			ExpectedTurnID string `json:"expectedTurnId"`
			Input          []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"input"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "turn/steer" || request.Params.ThreadID != "thread-current" || request.Params.ExpectedTurnID != "turn-current" {
		t.Fatalf("steer target = %#v", request)
	}
	if len(request.Params.Input) != 1 || request.Params.Input[0].Type != "text" || request.Params.Input[0].Text != "keep the original goal and add this" {
		t.Fatalf("steer input = %#v", request.Params.Input)
	}
}

func TestSupplementCodexTurnFailsClosedWithoutActiveTurn(t *testing.T) {
	c, stdin, _ := newTestCodexClient(t)
	c.threadID = "thread-current"
	if err := supplementCodexTurn(context.Background(), c, "extra"); err == nil {
		t.Fatal("supplementCodexTurn succeeded without an active turn")
	}
	if len(stdin.Lines()) != 0 {
		t.Fatalf("wrote a steer request without an active turn: %v", stdin.Lines())
	}
}

func TestCodexTokenUsageArrivingAfterTurnCompleted(t *testing.T) {
	c, _, _ := newTestCodexClient(t)
	c.threadID = "thread-1"
	c.setActiveTurnID("turn-current")
	c.handleRawNotification("turn/completed", map[string]any{
		"threadId": "thread-1", "turn": map[string]any{"id": "turn-current", "status": "completed"},
	})
	if c.activeTurnID() != "" {
		t.Fatal("completed turn still accepts supplements")
	}
	for _, turn := range []string{"turn-old", "turn-current", "turn-current"} {
		c.handleRawNotification("thread/tokenUsage/updated", codexThreadTokenUsageParams(
			"thread-1", turn, map[string]any{"inputTokens": float64(110)},
			map[string]any{"inputTokens": float64(100), "cachedInputTokens": float64(30), "outputTokens": float64(10)},
		))
	}
	if want := (TokenUsage{InputTokens: 70, CacheReadTokens: 30, OutputTokens: 10}); c.usage != want {
		t.Fatalf("late usage = %+v, want %+v", c.usage, want)
	}
}
