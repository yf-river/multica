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

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCodebuddyNativeFixtureProcess(t *testing.T) {
	if os.Getenv("MULTICA_CODEBUDDY_NATIVE_FIXTURE") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	paused := false
	danglingTool := os.Getenv("MULTICA_CODEBUDDY_NATIVE_DANGLING_TOOL") == "1"
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
				ClientCapabilities map[string]any `json:"clientCapabilities"`
				Action             string         `json:"action"`
				Condition          string         `json:"condition"`
				ExpectedRequestID  string         `json:"expectedRequestId"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "initialize":
			if capture := os.Getenv("MULTICA_CODEBUDDY_NATIVE_CAPABILITY_CAPTURE"); capture != "" {
				raw, _ := json.Marshal(request.Params.ClientCapabilities)
				if os.WriteFile(capture, raw, 0600) != nil {
					os.Exit(3)
				}
			}
		case "session/new", "session/load":
			result = map[string]any{"sessionId": "native-session", "models": map[string]string{"currentModelId": "flash"}}
		case codebuddyGoalControlMethod:
			if danglingTool && request.Params.Action == "set" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "native-session", "update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "unfinished-bash", "title": "Bash", "status": "in_progress", "rawInput": map[string]any{"command": "sleep 20"}}}})
			}
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

func TestCodebuddyNativeQuestionCapabilityMatchesResponder(t *testing.T) {
	for _, test := range []struct {
		name                   string
		responder, never, want bool
	}{
		{"interactive", true, false, true}, {"unattended", false, false, false}, {"no_approval", true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "capabilities.json")
			backend := &codebuddyBackend{cfg: Config{ExecutablePath: os.Args[0], LaunchPrefix: []string{"-test.run=^TestCodebuddyNativeFixtureProcess$", "--"}, Logger: slog.Default(), Env: map[string]string{"MULTICA_CODEBUDDY_NATIVE_FIXTURE": "1", "MULTICA_CODEBUDDY_NATIVE_CAPABILITY_CAPTURE": capture, "CODEBUDDY_CONFIG_DIR": t.TempDir()}}}
			opts := ExecOptions{Cwd: t.TempDir(), NativeGoal: true, Command: "/goal show", CommandOnly: true, Timeout: 5 * time.Second}
			if test.responder {
				opts.RequestInteraction = func(context.Context, protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
					return protocol.InteractionAnswer{Decision: "deny"}, nil
				}
			}
			if test.never {
				opts.ApprovalPolicy = "never"
			}
			session, err := backend.Execute(t.Context(), "", opts)
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			if result := <-session.Result; result.Status != "completed" {
				t.Fatalf("native control failed: %s", result.Error)
			}
			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			var caps struct {
				Meta struct {
					Codebuddy struct {
						Question bool `json:"question"`
					} `json:"codebuddy.ai"`
				} `json:"_meta"`
			}
			if err := json.Unmarshal(raw, &caps); err != nil {
				t.Fatal(err)
			}
			if caps.Meta.Codebuddy.Question != test.want {
				t.Fatalf("question capability=%v want %v", caps.Meta.Codebuddy.Question, test.want)
			}
		})
	}
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

func TestCodebuddyNativeGoalPauseInterruptsUnfinishedForegroundTool(t *testing.T) {
	backend := &codebuddyBackend{cfg: Config{ExecutablePath: os.Args[0], LaunchPrefix: []string{"-test.run=^TestCodebuddyNativeFixtureProcess$", "--"}, Logger: slog.Default(), Env: map[string]string{"MULTICA_CODEBUDDY_NATIVE_FIXTURE": "1", "MULTICA_CODEBUDDY_NATIVE_DANGLING_TOOL": "1", "CODEBUDDY_CONFIG_DIR": t.TempDir()}}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "", ExecOptions{Command: "/goal wait for confirmation", NativeGoal: true, Model: "flash", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	observed := false
	for message := range session.Messages {
		if !observed && message.Type == MessageToolUse {
			observed = true
			for !session.CommandReady() {
				select {
				case <-ctx.Done():
					t.Fatal("command gate unavailable")
				case <-time.After(time.Millisecond):
				}
			}
			output, err := session.RunCommand(ctx, "/goal pause")
			if err != nil || !strings.Contains(output, "暂停") {
				t.Fatalf("native pause was not confirmed: %q %v", output, err)
			}
		}
	}
	result := <-session.Result
	if !observed || result.Status != "cancelled" || !strings.Contains(result.Error, "stopped by user") {
		t.Fatalf("intentional goal pause was reported as a failure: observed=%v result=%+v", observed, result)
	}
	if session.CommandReady() {
		t.Fatal("interrupted execution still accepts commands")
	}
}
