package agent

import "testing"

func TestClaudeStreamResultUsage(t *testing.T) {
	tests := []struct {
		name          string
		message       claudeStreamMessage
		fallbackModel string
		want          map[string]TokenUsage
	}{
		{
			name: "model usage wins",
			message: claudeStreamMessage{
				Model: "ignored",
				Usage: &claudeStreamUsage{InputTokens: 99},
				ModelUsage: map[string]claudeStreamResultModelUsage{
					"claude-current": {InputTokens: 10, OutputTokens: 4, CacheReadInputTokens: 3, CacheCreationInputTokens: 2},
					"":               {InputTokens: 8},
					"empty":          {},
				},
			},
			want: map[string]TokenUsage{
				"claude-current": {InputTokens: 10, OutputTokens: 4, CacheReadTokens: 3, CacheWriteTokens: 2},
			},
		},
		{
			name:          "fallback model",
			message:       claudeStreamMessage{Usage: &claudeStreamUsage{OutputTokens: 7}},
			fallbackModel: "configured-model",
			want: map[string]TokenUsage{
				"configured-model": {OutputTokens: 7},
			},
		},
		{name: "empty usage", message: claudeStreamMessage{Model: "claude-current", Usage: &claudeStreamUsage{}}, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := claudeStreamResultUsage(tt.message, tt.fallbackModel)
			if len(got) != len(tt.want) {
				t.Fatalf("usage = %#v, want %#v", got, tt.want)
			}
			for model, want := range tt.want {
				if got[model] != want {
					t.Fatalf("usage[%q] = %#v, want %#v", model, got[model], want)
				}
			}
		})
	}
}
