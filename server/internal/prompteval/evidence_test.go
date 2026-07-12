package prompteval

import (
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
