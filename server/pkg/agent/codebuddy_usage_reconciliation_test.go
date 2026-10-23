package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodebuddyUsageProtocolPreservesObservedCalls(t *testing.T) {
	// 数字取自强来源81调用的完整聚合与平台原后9调用聚合；协议进程是受控fixture，不调用真实模型。
	full := TokenUsage{OutputTokens: 30496, CacheReadTokens: 6779648, CacheWriteTokens: 126990}
	tail := TokenUsage{OutputTokens: 4376, CacheReadTokens: 1105664, CacheWriteTokens: 4616}
	prefix := TokenUsage{OutputTokens: full.OutputTokens - tail.OutputTokens, CacheReadTokens: full.CacheReadTokens - tail.CacheReadTokens, CacheWriteTokens: full.CacheWriteTokens - tail.CacheWriteTokens}
	for _, mode := range []string{"smaller-summary", "different-model-identity", "complete-summary", "no-stream", "no-summary", "snapshot-update", "distinct-requests", "missing-identity"} {
		t.Run(mode, func(t *testing.T) {
			model := "deepseek-v4.1-flash"
			chosenModel := model
			var events []map[string]any
			assistant := func(request, id string, u TokenUsage) map[string]any {
				return map[string]any{"type": "assistant", "_requestId": request, "_messageId": id, "message": map[string]any{"role": "assistant", "model": model, "content": []map[string]any{{"type": "text", "text": "fixture"}}, "usage": map[string]any{"input_tokens": u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens, "output_tokens": u.OutputTokens, "cache_read_input_tokens": u.CacheReadTokens, "cache_creation_input_tokens": u.CacheWriteTokens}}}
			}
			summary := map[string]any{model: map[string]any{"inputTokens": tail.InputTokens, "outputTokens": tail.OutputTokens, "cacheReadInputTokens": tail.CacheReadTokens, "cacheCreationInputTokens": tail.CacheWriteTokens}}
			expected := full
			conflict := true
			switch mode {
			case "no-stream":
				expected = tail
				conflict = false
			case "snapshot-update":
				events = append(events, assistant("r", "m", tail), assistant("r", "m", tail), assistant("r", "m", full))
				summary = nil
				conflict = false
			case "distinct-requests":
				events = append(events, assistant("r1", "m", prefix), assistant("r2", "m", tail))
				summary = nil
				conflict = false
			case "missing-identity":
				events = append(events, assistant("", "", prefix), assistant("", "", tail))
				summary = nil
				conflict = false
			default:
				events = append(events, assistant("r", "m1", prefix), assistant("r", "m2", tail))
				if mode == "no-summary" {
					summary = nil
					conflict = false
				}
				if mode == "different-model-identity" {
					chosenModel = model + "-ioa"
					for _, event := range events {
						event["message"].(map[string]any)["model"] = chosenModel
					}
				}
				if mode == "complete-summary" {
					expected.OutputTokens++
					summary[model] = map[string]any{"inputTokens": expected.InputTokens, "outputTokens": expected.OutputTokens, "cacheReadInputTokens": expected.CacheReadTokens, "cacheCreationInputTokens": expected.CacheWriteTokens}
					conflict = false
				}
			}
			events = append(events, map[string]any{"type": "result", "session_id": "fixture-session", "result": "fixture complete", "modelUsage": summary})
			var script strings.Builder
			script.WriteString("#!/bin/sh\nIFS= read -r _\ncat <<'G51_USAGE_FIXTURE'\n")
			for _, event := range events {
				raw, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintln(&script, string(raw))
			}
			script.WriteString("G51_USAGE_FIXTURE\n")
			executable := filepath.Join(t.TempDir(), "provider-fixture")
			writeTestExecutable(t, executable, []byte(script.String()))
			var logs bytes.Buffer
			backend := &codebuddyBackend{cfg: Config{ExecutablePath: executable, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			session, err := backend.Execute(ctx, "fixture", ExecOptions{Model: model, Timeout: 3 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			result := <-session.Result
			if len(result.Usage) != 1 || result.Usage[chosenModel] != expected {
				t.Fatalf("mode=%s got=%+v want %s=%+v", mode, result.Usage, chosenModel, expected)
			}
			if strings.Contains(logs.String(), "codebuddy usage source conflict") != conflict {
				t.Fatalf("conflict evidence mismatch: %s", logs.String())
			}
			if conflict && (!strings.Contains(logs.String(), "stream_usage") || !strings.Contains(logs.String(), "result_usage") || !strings.Contains(logs.String(), "chosen_source")) {
				t.Fatalf("missing both source vectors: %s", logs.String())
			}
		})
	}
}
