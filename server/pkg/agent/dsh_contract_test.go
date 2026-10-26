package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDshBridgeGoldenContract(t *testing.T) {
	root := filepath.Join("..", "..", "..", "packages", "dsh-runtime", "fixtures")
	raw, err := os.ReadFile(filepath.Join(root, "execute.json"))
	if err != nil {
		t.Fatal(err)
	}
	var command dshExecuteCommand
	if err = json.Unmarshal(raw, &command); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Go command changed bridge fields: %s", encoded)
	}
	file, err := os.Open(filepath.Join(root, "frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	state := &dshRunState{usage: make(map[string]TokenUsage)}
	messages := make(chan Message, 32)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var frame dshFrame
		if err = json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatal(err)
		}
		handleDshFrame(frame, "contract-task", messages, state)
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	close(messages)
	if !state.ready || state.protocolError != "" || state.result == nil || state.result.Status != "completed" || state.result.SessionID != "contract-session" {
		t.Fatalf("bridge lifecycle did not survive Go decoding: %#v", state)
	}
	usage := state.result.Usage["deepseek-official/deepseek-flash"]
	if usage.InputTokens != 101 || usage.CacheReadTokens != 63 || usage.OutputTokens != 17 {
		t.Fatalf("usage fields drifted: %#v", usage)
	}
	contexts := 0
	for message := range messages {
		if message.Type != MessageRuntime {
			continue
		}
		var payload map[string]any
		if err = json.Unmarshal([]byte(message.Content), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["kind"] == "context" {
			contexts++
			if payload["duration_ms"] != float64(109) || payload["pressure_tokens"] != float64(164) || payload["breakdown"] == nil {
				t.Fatalf("context fields drifted: %s", message.Content)
			}
		}
	}
	if contexts != 1 {
		t.Fatalf("got %d context frames", contexts)
	}
}
