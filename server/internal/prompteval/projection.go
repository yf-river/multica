package prompteval

import (
	"unicode"

	"github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const agentRunHistoryLimit = 20

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

func PrependAgentRunHistory(raw any, latest map[string]any) []any {
	history, _ := raw.([]any)
	runID, _ := latest["run_id"].(string)
	next := []any{latest}
	for _, item := range history {
		if runID != "" {
			if existing, ok := item.(map[string]any); ok && existing["run_id"] == runID {
				continue
			}
		}
		next = append(next, item)
	}
	if len(next) > agentRunHistoryLimit {
		next = next[:agentRunHistoryLimit]
	}
	return next
}

func TruncateEvidence(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}

func ContainsHan(value string) bool {
	for _, current := range value {
		if unicode.Is(unicode.Han, current) {
			return true
		}
	}
	return false
}
