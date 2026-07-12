package prompteval

const agentRunHistoryLimit = 20

// PrependAgentRunHistory keeps one latest snapshot per Run and bounds the
// denormalized Asset history shared by create and terminal-sync projections.
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
