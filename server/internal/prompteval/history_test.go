package prompteval

import (
	"fmt"
	"testing"
)

func TestPrependAgentRunHistoryReplacesRunAndBoundsHistory(t *testing.T) {
	history := make([]any, 0, 21)
	history = append(history, map[string]any{"run_id": "run-current", "status": "queued"})
	for index := 0; index < 20; index++ {
		history = append(history, map[string]any{"run_id": fmt.Sprintf("run-%d", index)})
	}
	latest := map[string]any{"run_id": "run-current", "status": "completed"}

	result := PrependAgentRunHistory(history, latest)
	first, firstOK := result[0].(map[string]any)
	if len(result) != agentRunHistoryLimit || !firstOK || first["status"] != "completed" {
		t.Fatalf("history length/latest = %d %#v", len(result), result[0])
	}
	duplicates := 0
	for _, item := range result {
		if row, ok := item.(map[string]any); ok && row["run_id"] == "run-current" {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("run-current entries = %d, want 1", duplicates)
	}
}
