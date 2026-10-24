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
	for _, mode := range []string{"smaller-summary", "different-model-identity", "session-cumulative-summary", "no-stream", "resumed-no-stream", "empty-result-session", "assistant-session", "no-summary", "snapshot-update", "distinct-requests", "missing-identity"} {
		t.Run(mode, func(t *testing.T) {
			// 夹具会话只存在于协议流里，本地没有对应记录。用显式的空配置目录
			// 把判定固定成"未知"，而不是依赖宿主的 HOME（也不去改进程环境，
			// 免得影响同包并行用例）。
			emptyConfigDir := t.TempDir()
			model := "deepseek-v4.1-flash"
			chosenModel := model
			events := []map[string]any{{"type": "system", "subtype": "init", "session_id": "fixture-session"}}
			assistant := func(request, id string, u TokenUsage) map[string]any {
				return map[string]any{"type": "assistant", "_requestId": request, "_messageId": id, "message": map[string]any{"role": "assistant", "model": model, "content": []map[string]any{{"type": "text", "text": "fixture"}}, "usage": map[string]any{"input_tokens": u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens, "output_tokens": u.OutputTokens, "cache_read_input_tokens": u.CacheReadTokens, "cache_creation_input_tokens": u.CacheWriteTokens}}}
			}
			summary := map[string]any{model: map[string]any{"inputTokens": tail.InputTokens, "outputTokens": tail.OutputTokens, "cacheReadInputTokens": tail.CacheReadTokens, "cacheCreationInputTokens": tail.CacheWriteTokens}}
			expected := full
			conflict := true
			switch mode {
			case "no-stream", "resumed-no-stream":
				expected = tail
				conflict = mode == "resumed-no-stream"
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
					for _, event := range events[1:] {
						event["message"].(map[string]any)["model"] = chosenModel
					}
				}
				if mode == "session-cumulative-summary" {
					// 实际续接摘要包含前轮；同模型键的更大摘要也不是本轮用量。
					summary[model] = map[string]any{"inputTokens": 0, "outputTokens": full.OutputTokens + tail.OutputTokens, "cacheReadInputTokens": full.CacheReadTokens + tail.CacheReadTokens, "cacheCreationInputTokens": full.CacheWriteTokens + tail.CacheWriteTokens}
				}
			}
			resultSession := "fixture-session"
			if mode == "empty-result-session" || mode == "assistant-session" {
				resultSession = ""
			}
			if mode == "assistant-session" {
				events = events[1:]
				events[0]["session_id"] = "fixture-session"
			}
			events = append(events, map[string]any{"type": "result", "session_id": resultSession, "result": "fixture complete", "modelUsage": summary})
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
			backend := &codebuddyBackend{cfg: Config{
				ExecutablePath: executable,
				Logger:         slog.New(slog.NewJSONHandler(&logs, nil)),
				Env:            map[string]string{"CODEBUDDY_CONFIG_DIR": emptyConfigDir},
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			opts := ExecOptions{Model: model, Timeout: 3 * time.Second}
			if mode == "resumed-no-stream" || mode == "session-cumulative-summary" {
				opts.ResumeSessionID = "fixture-session"
			}
			session, err := backend.Execute(ctx, "fixture", opts)
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			result := <-session.Result
			if result.SessionID != "fixture-session" {
				t.Fatalf("lost actual SDK session: %+v", result)
			}
			if mode == "resumed-no-stream" {
				if len(result.Usage) != 0 {
					t.Fatalf("session cumulative summary reported as current run: %+v", result.Usage)
				}
			} else if len(result.Usage) != 1 || result.Usage[chosenModel] != expected {
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

func TestCodebuddyCancelledStreamRetainsObservedAccounting(t *testing.T) {
	// 受控SDK进程在首个已计量assistant后阻塞，正常取消只收回已观测数据。
	script := `#!/bin/sh
IFS= read -r _
printf '%s\n' '{"type":"system","subtype":"init","session_id":"observed-session"}'
printf '%s\n' '{"type":"assistant","session_id":"observed-session","_requestId":"observed-request","_messageId":"observed-call","message":{"role":"assistant","model":"flash-observed","content":[{"type":"text","text":"cancel-ready"}],"usage":{"input_tokens":15,"output_tokens":7,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}'
printf '%s\n' '{"type":"system","subtype":"status","session_id":"auxiliary-session"}'
exec sleep 30
`
	executable := filepath.Join(t.TempDir(), "provider-fixture")
	writeTestExecutable(t, executable, []byte(script))
	backend := &codebuddyBackend{cfg: Config{ExecutablePath: executable, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := backend.Execute(ctx, "fixture", ExecOptions{Model: "flash-requested", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	observed := false
	for msg := range session.Messages {
		if msg.Type == MessageStatus && msg.SessionID != "" && observed {
			if msg.SessionID != "observed-session" {
				t.Fatalf("auxiliary event replaced root: %+v", msg)
			}
			cancel()
		}
		if msg.Content == "cancel-ready" {
			observed = true
		}
	}
	result := <-session.Result
	want := TokenUsage{OutputTokens: 7, CacheReadTokens: 10, CacheWriteTokens: 5}
	if !observed || result.Status != "aborted" || result.SessionID != "observed-session" || result.ProviderTraceID != "observed-request" || result.Usage["flash-observed"] != want {
		t.Fatalf("cancel lost SDK accounting: observed=%v result=%+v", observed, result)
	}
}
