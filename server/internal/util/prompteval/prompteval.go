// Package prompteval provides prompt-evaluation helpers shared by the handler
// and service layers.
package prompteval

import (
	"encoding/json"
	"strconv"
	"unicode"
)

// TruncatePromptEvaluationEvidence truncates evidence by rune and appends an ellipsis.
func TruncatePromptEvaluationEvidence(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}

// MustJSONBytes marshals a value as JSON and returns an empty object on failure.
func MustJSONBytes(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// DecodePayloadObject decodes a JSON object and returns an empty object for
// empty or invalid input.
func DecodePayloadObject(raw []byte) map[string]any {
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

// ContainsHanRune reports whether a string contains a Han character.
func ContainsHanRune(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// AppendPromptEvaluationAgentRunHistory prepends an agent run, deduplicates by
// run_id, and retains at most 20 entries.
func AppendPromptEvaluationAgentRunHistory(raw any, result map[string]any) []any {
	history, _ := raw.([]any)
	runID := StringFromAny(result["run_id"])
	next := []any{result}
	for _, item := range history {
		if runID != "" {
			if existing, ok := item.(map[string]any); ok && StringFromAny(existing["run_id"]) == runID {
				continue
			}
		}
		next = append(next, item)
	}
	if len(next) > 20 {
		next = next[:20]
	}
	return next
}

// StringFromAny converts common scalar types to strings.
func StringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}
