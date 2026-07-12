package prompteval

import (
	"github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// UsageEvidenceRows projects persisted task usage into the current evaluation
// evidence contract. Keep this projection shared so candidate and run evidence
// cannot drift in fields or cost precision.
func UsageEvidenceRows(usages []db.TaskUsage) []map[string]any {
	rows := make([]map[string]any, 0, len(usages))
	for _, usage := range usages {
		breakdown, priced := metrics.EstimateUsageCostBreakdownUSD(
			usage.Provider,
			usage.Model,
			usage.InputTokens,
			usage.OutputTokens,
			usage.CacheReadTokens,
			usage.CacheWriteTokens,
		)
		rows = append(rows, map[string]any{
			"provider":           usage.Provider,
			"model":              usage.Model,
			"input_tokens":       usage.InputTokens,
			"output_tokens":      usage.OutputTokens,
			"cache_read_tokens":  usage.CacheReadTokens,
			"cache_write_tokens": usage.CacheWriteTokens,
			"estimated_cost":     metrics.RoundCostUSD(breakdown.TotalCostUSD),
			"priced":             priced,
		})
	}
	return rows
}
