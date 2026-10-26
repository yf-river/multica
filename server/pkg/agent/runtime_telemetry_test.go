package agent

import (
	"encoding/json"
	"testing"
)

func TestCodexContextTelemetryAttribution(t *testing.T) {
	c, _, _ := newTestCodexClient(t)
	var messages []Message
	c.onMessage = func(msg Message) { messages = append(messages, msg) }
	c.threadID = "thread"
	c.setActiveTurnID("current")
	params := codexThreadTokenUsageParams("thread", "current",
		map[string]any{"inputTokens": float64(100), "cachedInputTokens": float64(40), "outputTokens": float64(10), "totalTokens": float64(110)},
		map[string]any{"inputTokens": float64(100), "cachedInputTokens": float64(40), "outputTokens": float64(10), "totalTokens": float64(110)},
	)
	params["tokenUsage"].(map[string]any)["modelContextWindow"] = float64(1000)
	c.updateThreadTokenUsage(params)
	c.updateThreadTokenUsage(params)
	params["turnId"] = "historical"
	c.updateThreadTokenUsage(params)
	var events []map[string]any
	for _, msg := range messages {
		if msg.Type != MessageRuntime {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(msg.Content), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("duplicated or replayed telemetry: %+v", events)
	}
	e := events[0]
	if e["input_tokens"] != float64(60) || e["cache_read_tokens"] != float64(40) || e["pressure_tokens"] != float64(110) || e["context_window"] != float64(1000) {
		t.Fatalf("incorrect telemetry: %+v", e)
	}
	if _, exists := e["duration_ms"]; exists {
		t.Fatal("invented model duration")
	}
	if _, exists := e["breakdown"]; exists {
		t.Fatal("invented context breakdown")
	}
}
