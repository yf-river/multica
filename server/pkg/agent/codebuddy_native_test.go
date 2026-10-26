package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	hasGoal, faulted := true, false
	childFixture := os.Getenv("MULTICA_CODEBUDDY_NATIVE_CHILD_FIXTURE") == "1"
	if childFixture {
		hasGoal = false
		if os.Getenv(codebuddyDisableBackgroundTasksEnv) != "1" {
			os.Exit(3)
		}
	}
	var promptID json.RawMessage
	condition := "fixture goal"
	fault := os.Getenv("MULTICA_CODEBUDDY_NATIVE_CONTROL_FAULT")
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Action            string `json:"action"`
				Condition         string `json:"condition"`
				ExpectedRequestID string `json:"expectedRequestId"`
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
			if request.Params.Action == "set" && request.Params.Condition != "" {
				condition, hasGoal = request.Params.Condition, true
			}
			if request.Params.Action == "clear" {
				hasGoal = false
			}
			if request.Params.Action == "pause" {
				paused = true
			}
			if request.Params.Action == "resume" || request.Params.Action == "set" {
				paused = false
			}
			result = map[string]any{"ok": true, "applied": request.Params.Action, "hasGoal": hasGoal, "runInFlight": false, "activeGoal": map[string]any{"condition": condition, "paused": paused}}
			if !hasGoal {
				delete(result, "activeGoal")
				result["recap"] = map[string]any{"latest": map[string]any{"ok": false, "reason": "historical failed goal"}}
			}
			if fault != "" && ((request.Params.Action == "pause" && strings.HasPrefix(fault, "pause")) || (request.Params.Action == "clear" && fault == "clear_ack_lost")) {
				faulted = true
				result = map[string]any{}
			}
			if faulted && fault == "pause_unavailable" && request.Params.Action == "snapshot" {
				result = map[string]any{}
			}
			if request.Params.Action == "pause" {
				_ = os.WriteFile(os.Getenv("MULTICA_CODEBUDDY_NATIVE_PAUSE_MARKER"), []byte("paused"), 0600)
			}
		case "session/prompt":
			if childFixture {
				promptID = append(json.RawMessage(nil), request.ID...)
				for _, frame := range []struct{ kind, text, cid, parent string }{
					{"agent_message_chunk", "parent answer", "parent-request", ""},
					{"agent_message_chunk", "child answer", "child-request", "agent-call"},
					{"turn_end", "", "child-request", "agent-call"},
				} {
					_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "native-session", "update": map[string]any{"sessionUpdate": frame.kind, "content": map[string]string{"type": "text", "text": frame.text}, "_meta": map[string]string{"codebuddy.ai/requestId": frame.cid, "codebuddy.ai/parentToolCallId": frame.parent}}}})
				}
				continue
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "native-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "cancel-ready"}, "_meta": map[string]string{"codebuddy.ai/requestId": "native-request"}}}})
			continue
		case "session/steer":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"steered": request.Params.ExpectedRequestID == "parent-request", "ownerRequestId": request.Params.ExpectedRequestID}})
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": promptID, "result": map[string]any{"stopReason": "end_turn"}})
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
	os.Exit(0)
}

func TestCodebuddyNativeChildDoesNotReplaceParentTurn(t *testing.T) {
	t.Setenv(codebuddyDisableBackgroundTasksEnv, "0")
	backend := &codebuddyBackend{cfg: Config{ExecutablePath: os.Args[0], LaunchPrefix: []string{"-test.run=^TestCodebuddyNativeFixtureProcess$", "--"}, Logger: slog.Default(), Env: map[string]string{"MULTICA_CODEBUDDY_NATIVE_FIXTURE": "1", "MULTICA_CODEBUDDY_NATIVE_CHILD_FIXTURE": "1", "CODEBUDDY_CONFIG_DIR": t.TempDir(), codebuddyDisableBackgroundTasksEnv: "0"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "continue", ExecOptions{NativeGoal: true, Model: "flash", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	childVisible := false
	for msg := range session.Messages {
		if msg.Type == MessageText && msg.Content == "child answer" {
			childVisible = true
			if err := session.Supplement(ctx, "update the parent"); err != nil {
				t.Errorf("child replaced the parent's steer target: %v", err)
				cancel()
			}
		}
	}
	result := <-session.Result
	if !childVisible || result.Status != "completed" || result.ProviderTraceID != "parent-request" || result.Output != "parent answer" {
		t.Fatalf("child changed parent identity or final answer: child=%v result=%+v", childVisible, result)
	}
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

func TestCodebuddyNativeReconcilesUncertainGoalControl(t *testing.T) {
	for _, fault := range []string{"pause_ack_lost", "clear_ack_lost", "pause_unavailable"} {
		t.Run(fault, func(t *testing.T) {
			backend := &codebuddyBackend{cfg: Config{ExecutablePath: os.Args[0], LaunchPrefix: []string{"-test.run=^TestCodebuddyNativeFixtureProcess$", "--"}, Logger: slog.Default(), Env: map[string]string{"MULTICA_CODEBUDDY_NATIVE_FIXTURE": "1", "MULTICA_CODEBUDDY_NATIVE_CONTROL_FAULT": fault, "CODEBUDDY_CONFIG_DIR": t.TempDir()}}}
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
			command := "/goal pause"
			if fault == "clear_ack_lost" {
				command = "/goal clear"
			}
			if _, err := session.RunCommand(ctx, command); err == nil {
				t.Fatal("unconfirmed command was reported as confirmed")
			}
			result := <-session.Result
			<-drained
			if fault == "pause_unavailable" {
				if result.Status != "failed" || !strings.Contains(result.Error, "could not be verified") {
					t.Fatalf("unverified state reported success: %+v", result)
				}
			} else if result.Status != "completed" {
				t.Fatalf("verified native stop inherited a historical failure: %+v", result)
			}
			if session.CommandReady() {
				t.Fatal("terminal execution still accepts commands")
			}
		})
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
