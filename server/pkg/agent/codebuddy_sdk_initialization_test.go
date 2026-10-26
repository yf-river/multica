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

func TestCodebuddySDKInitializationFixtureProcess(t *testing.T) {
	mode := os.Getenv("CODEBUDDY_SDK_INITIALIZATION_FIXTURE")
	if mode == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	if !scanner.Scan() {
		os.Exit(3)
	}
	var request struct {
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Request.Subtype != "initialize" {
		os.Exit(4)
	}
	if mode != "no_ack" {
		if mode == "reject_after_child_ack" {
			_ = encoder.Encode(map[string]any{"type": "control_response", "parent_tool_use_id": "child", "response": map[string]string{"subtype": "success", "request_id": request.RequestID}})
		}
		subtype := "success"
		if mode != "success" {
			subtype = "error"
		}
		_ = encoder.Encode(map[string]any{"type": "control_response", "response": map[string]string{"subtype": subtype, "request_id": request.RequestID, "error": "fixture SDK rejected"}})
	}
	if scanner.Scan() {
		_ = os.WriteFile(os.Getenv("CODEBUDDY_SDK_PROMPT_CAPTURE"), scanner.Bytes(), 0600)
		_ = encoder.Encode(map[string]any{"type": "system", "session_id": "sdk-session"})
		_ = encoder.Encode(map[string]any{"type": "result", "session_id": "sdk-session", "result": "SDK_READY", "is_error": false})
	}
	os.Exit(0)
}

func TestCodebuddySDKInitializationGatesPromptSubmission(t *testing.T) {
	for _, mode := range []string{"success", "reject_after_child_ack", "no_ack"} {
		t.Run(mode, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "prompt.json")
			backend := &codebuddyBackend{cfg: Config{ExecutablePath: os.Args[0], LaunchPrefix: []string{"-test.run=^TestCodebuddySDKInitializationFixtureProcess$", "--"}, Logger: slog.Default(), Env: map[string]string{"CODEBUDDY_SDK_INITIALIZATION_FIXTURE": mode, "CODEBUDDY_SDK_PROMPT_CAPTURE": capture, "CODEBUDDY_CONFIG_DIR": t.TempDir()}}}
			opts := ExecOptions{Cwd: t.TempDir(), Timeout: 5 * time.Second, ThreadHandshakeTimeout: time.Second, RequestInteraction: func(context.Context, protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
				return protocol.InteractionAnswer{Decision: "deny"}, nil
			}}
			session, err := backend.Execute(t.Context(), "owned prompt", opts)
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			result := <-session.Result
			raw, readErr := os.ReadFile(capture)
			if mode == "success" {
				if result.Status != "completed" || result.Output != "SDK_READY" || readErr != nil || !strings.Contains(string(raw), "owned prompt") {
					t.Fatalf("acknowledged SDK did not execute: %+v prompt=%v", result, readErr)
				}
			} else if result.Status != "failed" || !strings.Contains(result.Error, "initialize codebuddy SDK") || !os.IsNotExist(readErr) {
				t.Fatalf("unconfirmed SDK submitted a prompt or masked initialization failure: %+v prompt=%v", result, readErr)
			}
		})
	}
}
