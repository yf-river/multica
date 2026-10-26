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

func TestCodexToolDurationUsesReportedFieldsOnce(t *testing.T) {
	c, _, _ := newTestCodexClient(t)
	count := 0
	c.onMessage = func(msg Message) {
		if msg.Type == MessageRuntime {
			count++
		}
	}
	params := map[string]any{"turnId": "turn"}
	c.emitToolDuration(params, map[string]any{"id": "missing"})
	c.emitToolDuration(params, map[string]any{"id": "tool", "durationMs": float64(0)})
	c.emitToolDuration(params, map[string]any{"id": "tool", "durationMs": float64(0)})
	if count != 1 {
		t.Fatalf("missing or duplicate duration: %d", count)
	}
}

func TestCodebuddyACPContextDoesNotInventUsage(t *testing.T) {
	var messages []Message
	emitCodebuddyACPContext(`{"params":{"sessionId":"session","update":{"used":123,"size":1000,"_meta":{"codebuddy.ai/usageByCategory":{"systemPrompt":10,"tools":0,"conversation":100,"mcp":8,"skills":5,"version":1}}}}}`, func(msg Message) { messages = append(messages, msg) })
	if len(messages) != 1 {
		t.Fatal("snapshot missing")
	}
	var event map[string]any
	if json.Unmarshal([]byte(messages[0].Content), &event) != nil {
		t.Fatal("snapshot invalid")
	}
	if event["pressure_tokens"] != float64(123) || event["context_window"] != float64(1000) || event["input_tokens"] != nil || event["duration_ms"] != nil {
		t.Fatalf("snapshot mixed with accounting: %+v", event)
	}
	breakdown := event["breakdown"].(map[string]any)
	if breakdown["tools_tokens"] != float64(0) || breakdown["mcp_tokens"] != float64(8) {
		t.Fatal("reported category values lost")
	}
}
