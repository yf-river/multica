package prompteval

import (
	"fmt"
	"reflect"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestUsageEvidenceRowsUsesOneStableContract(t *testing.T) {
	rows := UsageEvidenceRows([]db.TaskUsage{
		{
			Provider:         "codebuddy",
			Model:            "deepseek-v4-pro-ioa",
			InputTokens:      59_738,
			OutputTokens:     186,
			CacheReadTokens:  29_440,
			CacheWriteTokens: 30_298,
		},
		{
			Provider:     "custom",
			Model:        "unpriced-model",
			InputTokens:  12,
			OutputTokens: 3,
		},
	})

	want := []map[string]any{
		{
			"provider":           "codebuddy",
			"model":              "deepseek-v4-pro-ioa",
			"input_tokens":       int64(59_738),
			"output_tokens":      int64(186),
			"cache_read_tokens":  int64(29_440),
			"cache_write_tokens": int64(30_298),
			"estimated_cost":     0.013449,
			"priced":             true,
		},
		{
			"provider":           "custom",
			"model":              "unpriced-model",
			"input_tokens":       int64(12),
			"output_tokens":      int64(3),
			"cache_read_tokens":  int64(0),
			"cache_write_tokens": int64(0),
			"estimated_cost":     float64(0),
			"priced":             false,
		},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %#v, want %#v", rows, want)
	}
}

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

func TestTextProjections(t *testing.T) {
	if got := TruncateEvidence("证据完整", 2); got != "证据..." {
		t.Fatalf("TruncateEvidence() = %q", got)
	}
	if got := TruncateEvidence("ok", 2); got != "ok" {
		t.Fatalf("TruncateEvidence() unchanged = %q", got)
	}
	if !ContainsHan("agent 证据") || ContainsHan("agent evidence") {
		t.Fatal("ContainsHan() did not preserve the Han-script boundary")
	}
}
