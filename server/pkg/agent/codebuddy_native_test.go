package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodebuddyNativeFixtureProcess(t *testing.T) {
	if os.Getenv("MULTICA_CODEBUDDY_NATIVE_FIXTURE") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Action string `json:"action"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "session/new", "session/load":
			result = map[string]any{"sessionId": "native-session", "models": map[string]string{"currentModelId": "flash"}}
		case codebuddyGoalControlMethod:
			result = map[string]any{"ok": true, "applied": request.Params.Action, "hasGoal": true, "runInFlight": false, "activeGoal": map[string]any{"condition": "fixture goal", "paused": request.Params.Action == "pause"}}
			if request.Params.Action == "pause" {
				_ = os.WriteFile(os.Getenv("MULTICA_CODEBUDDY_NATIVE_PAUSE_MARKER"), []byte("paused"), 0600)
			}
		case "session/prompt":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "native-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "cancel-ready"}, "_meta": map[string]string{"codebuddy.ai/requestId": "native-request"}}}})
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
	os.Exit(0)
}

func TestCodebuddyNativeCancellationPausesGoalBeforeProcessExit(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "goal-paused")
	backend := &codebuddyBackend{cfg: Config{ExecutablePath: os.Args[0], LaunchPrefix: []string{"-test.run=^TestCodebuddyNativeFixtureProcess$", "--"}, Logger: slog.Default(), Env: map[string]string{"MULTICA_CODEBUDDY_NATIVE_FIXTURE": "1", "MULTICA_CODEBUDDY_NATIVE_PAUSE_MARKER": marker, "CODEBUDDY_CONFIG_DIR": t.TempDir()}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "continue", ExecOptions{NativeGoal: true, Model: "flash", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	observed := false
	for msg := range session.Messages {
		if msg.Content == "cancel-ready" {
			observed = true
			cancel()
		}
	}
	result := <-session.Result
	if !observed || result.Status != "aborted" {
		t.Fatalf("cancellation failed: observed=%v result=%+v", observed, result)
	}
	saved, err := os.ReadFile(marker)
	if err != nil || string(saved) != "paused" {
		t.Fatalf("native goal was not paused before shutdown: %v", err)
	}
}
