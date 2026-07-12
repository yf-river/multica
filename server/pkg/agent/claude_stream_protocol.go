package agent

import "encoding/json"

// Claude and CodeBuddy currently expose the same stream-json wire protocol.
// Process lifecycle and provider-specific control behavior stay in each
// backend; this file owns only the shared decoded data contract.
type claudeStreamMessage struct {
	Type      string          `json:"type"`
	Message   json.RawMessage `json:"message,omitempty"`
	Subtype   string          `json:"subtype,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Model     string          `json:"model,omitempty"`

	ResultText string                                  `json:"result,omitempty"`
	IsError    bool                                    `json:"is_error,omitempty"`
	DurationMs float64                                 `json:"duration_ms,omitempty"`
	NumTurns   int                                     `json:"num_turns,omitempty"`
	Usage      *claudeStreamUsage                      `json:"usage,omitempty"`
	ModelUsage map[string]claudeStreamResultModelUsage `json:"modelUsage,omitempty"`

	Log *claudeStreamLogEntry `json:"log,omitempty"`

	RequestID string          `json:"request_id,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`
}

type claudeStreamLogEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type claudeStreamMessageContent struct {
	Role    string                     `json:"role"`
	Model   string                     `json:"model"`
	Content []claudeStreamContentBlock `json:"content"`
	Usage   *claudeStreamUsage         `json:"usage,omitempty"`
}

type claudeStreamUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type claudeStreamResultModelUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

type claudeStreamContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type claudeStreamControlRequest struct {
	Subtype  string          `json:"subtype"`
	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

func claudeStreamResultUsage(msg claudeStreamMessage, fallbackModel string) map[string]TokenUsage {
	if len(msg.ModelUsage) > 0 {
		usage := make(map[string]TokenUsage, len(msg.ModelUsage))
		for model, item := range msg.ModelUsage {
			if model == "" || !claudeStreamUsageHasTokens(item.InputTokens, item.OutputTokens, item.CacheReadInputTokens, item.CacheCreationInputTokens) {
				continue
			}
			usage[model] = TokenUsage{
				InputTokens:      item.InputTokens,
				OutputTokens:     item.OutputTokens,
				CacheReadTokens:  item.CacheReadInputTokens,
				CacheWriteTokens: item.CacheCreationInputTokens,
			}
		}
		if len(usage) > 0 {
			return usage
		}
	}

	model := msg.Model
	if model == "" {
		model = fallbackModel
	}
	if msg.Usage == nil || model == "" || !claudeStreamUsageHasTokens(
		msg.Usage.InputTokens,
		msg.Usage.OutputTokens,
		msg.Usage.CacheReadInputTokens,
		msg.Usage.CacheCreationInputTokens,
	) {
		return nil
	}
	return map[string]TokenUsage{
		model: {
			InputTokens:      msg.Usage.InputTokens,
			OutputTokens:     msg.Usage.OutputTokens,
			CacheReadTokens:  msg.Usage.CacheReadInputTokens,
			CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
		},
	}
}

func claudeStreamUsageHasTokens(input, output, cacheRead, cacheWrite int64) bool {
	return input > 0 || output > 0 || cacheRead > 0 || cacheWrite > 0
}
