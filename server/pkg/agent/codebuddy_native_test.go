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
	paused := false
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
			if request.Params.Action == "pause" {
				paused = true
			}
			if request.Params.Action == "resume" || request.Params.Action == "set" {
				paused = false
			}
			result = map[string]any{"ok": true, "applied": request.Params.Action, "hasGoal": true, "runInFlight": false, "activeGoal": map[string]any{"condition": "fixture goal", "paused": paused}}
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

func TestCodebuddyNativeLivePauseEndsIdleGoal(t *testing.T) {
	backend := &codebuddyBackend{cfg: Config{ExecutablePath: os.Args[0], LaunchPrefix: []string{"-test.run=^TestCodebuddyNativeFixtureProcess$", "--"}, Logger: slog.Default(), Env: map[string]string{"MULTICA_CODEBUDDY_NATIVE_FIXTURE": "1", "CODEBUDDY_CONFIG_DIR": t.TempDir()}}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "", ExecOptions{NativeGoal: true, Command: "/goal fixture goal", Model: "flash", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range session.Messages {
		}
	}()
	for !session.CommandReady() {
		select {
		case <-ctx.Done():
			t.Fatal("native command gate did not open")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := session.RunCommand(ctx, "/plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.RunCommand(ctx, "/goal pause"); err != nil {
		t.Fatal(err)
	}
	result := <-session.Result
	<-drained
	if result.Status != "completed" || session.CommandReady() {
		t.Fatalf("paused idle goal did not finish: %+v", result)
	}
	if _, err := session.RunCommand(ctx, "/goal resume"); err == nil {
		t.Fatal("terminal execution accepted a new goal")
	}
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
