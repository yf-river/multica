package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCodebuddyGoalCommandMapping(t *testing.T) {
	for _, test := range []struct{ command, action, condition string }{
		{"/goal show", "snapshot", ""}, {"/goal pause", "pause", ""}, {"/goal resume", "resume", ""}, {"/goal clear", "clear", ""}, {"/goal complete", "clear", ""}, {"/goal implement the feature", "set", "implement the feature"},
	} {
		action, condition, err := codebuddyGoalAction(test.command)
		if err != nil || action != test.action || condition != test.condition {
			t.Fatalf("%q => %q %q %v", test.command, action, condition, err)
		}
	}
}

func TestCodebuddyACPQuestionUsesNativeQuestionIDs(t *testing.T) {
	stdin := &fakeStdinWithHook{}
	client := &hermesClient{stdin: stdin}
	broker := newInteractionBroker("", context.Background(), func(_ context.Context, prompt protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
		if prompt.Questions[0].ID != "q_0" || !prompt.Questions[0].MultiSelect {
			t.Errorf("question identity changed: %+v", prompt)
		}
		return protocol.InteractionAnswer{Decision: "allow", Answers: map[string][]string{"q_0": {"A", "B"}}}, nil
	}, nil)
	defer broker.stop()
	if !handleCodebuddyACPInteraction(client, broker, "session", "_codebuddy.ai/question", json.RawMessage(`"native-request"`), json.RawMessage(`{"sessionId":"session","inputType":"question","schema":{"questions":[{"id":"q_0","question":"Choose","multiSelect":true}]}}`)) {
		t.Fatal("question not bridged")
	}
	broker.wg.Wait()
	var response struct {
		Result struct {
			Outcome string              `json:"outcome"`
			Answers map[string][]string `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdin.Lines()[0]), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.Outcome != "submitted" || len(response.Result.Answers["q_0"]) != 2 {
		t.Fatalf("native answer changed: %+v", response)
	}
}

func TestCodebuddyNativeAskForwardsAnswersBeforeResolvingPermission(t *testing.T) {
	for _, test := range []struct {
		name, decision, option string
		resolved               bool
	}{
		{"allow", "allow", "allow", true}, {"deny", "deny", "reject", true}, {"stale_native_request", "allow", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdin := &fakeStdinWithHook{}
			client := &hermesClient{stdin: stdin, cfg: Config{Logger: slog.Default()}, pending: make(map[int]*pendingRPC)}
			stdin.afterWrite = func() {
				lines := stdin.Lines()
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						SessionID  string              `json:"sessionId"`
						ToolCallID string              `json:"toolCallId"`
						Decision   string              `json:"decision"`
						Answers    map[string][]string `json:"answers"`
					} `json:"params"`
				}
				if err := json.Unmarshal([]byte(lines[len(lines)-1]), &request); err != nil {
					t.Error(err)
					return
				}
				if request.Method != "_codebuddy.ai/resolveInterruption" {
					return
				}
				wantDecision := "reject"
				if test.decision == "allow" {
					wantDecision = "allow"
				}
				if request.Params.SessionID != "session" || request.Params.ToolCallID != "native-tool" || request.Params.Decision != wantDecision || test.decision == "allow" && len(request.Params.Answers["q_0"]) != 2 {
					t.Errorf("native question delivery changed: %+v", request.Params)
				}
				if test.decision != "allow" && len(request.Params.Answers) != 0 {
					t.Error("declined question forwarded answer contents")
				}
				response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]bool{"resolved": test.resolved}})
				client.handleLine(string(response))
			}
			broker := newInteractionBroker("codebuddy", t.Context(), func(_ context.Context, prompt protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
				if prompt.Kind != "question" || len(prompt.Questions) != 1 || prompt.Questions[0].ID != "q_0" || !prompt.Questions[0].MultiSelect {
					t.Errorf("native Ask became an approval: %+v", prompt)
				}
				return protocol.InteractionAnswer{Decision: test.decision, Answers: map[string][]string{"q_0": {"A", "B"}}}, nil
			}, nil)
			defer broker.stop()
			raw := json.RawMessage(`{"sessionId":"session","toolCall":{"toolCallId":"native-tool","_meta":{"codebuddy.ai/toolName":"AskUserQuestion"},"rawInput":{"questions":[{"question":"Choose","multiSelect":true,"options":[{"label":"A"},{"label":"B"}]}]}},"options":[{"kind":"allow_once","optionId":"allow"},{"kind":"reject_once","optionId":"reject"}]}`)
			if !handleCodebuddyACPInteraction(client, broker, "session", "session/request_permission", json.RawMessage(`"permission-id"`), raw) {
				t.Fatal("native question was not handled")
			}
			broker.wg.Wait()
			lines := stdin.Lines()
			if len(lines) != 2 {
				t.Fatalf("expected native delivery followed by permission result: %v", lines)
			}
			var response struct {
				Error  json.RawMessage `json:"error"`
				Result struct {
					Outcome struct {
						OptionID string `json:"optionId"`
					} `json:"outcome"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(lines[1]), &response); err != nil {
				t.Fatal(err)
			}
			if test.resolved && response.Result.Outcome.OptionID != test.option || !test.resolved && len(response.Error) == 0 {
				t.Fatalf("native resolution acknowledgement was ignored: %s", lines[1])
			}
		})
	}
}

func TestCodebuddyACPApprovalCannotBecomePermanent(t *testing.T) {
	stdin := &fakeStdinWithHook{}
	client := &hermesClient{stdin: stdin}
	broker := newInteractionBroker("", context.Background(), func(context.Context, protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
		return protocol.InteractionAnswer{Decision: "allow"}, nil
	}, nil)
	defer broker.stop()
	handleCodebuddyACPInteraction(client, broker, "session", "session/request_permission", json.RawMessage(`1`), json.RawMessage(`{"sessionId":"session","toolCall":{"title":"Run command"},"options":[{"kind":"allow_always","optionId":"forever"}]}`))
	broker.wg.Wait()
	var response map[string]json.RawMessage
	_ = json.Unmarshal([]byte(stdin.Lines()[0]), &response)
	if response["error"] == nil {
		t.Fatal("one-time approval expanded to permanent grant")
	}
}

func TestCodebuddyACPPlanShowsNativeArtifactAndKeepsRejectionInPlan(t *testing.T) {
	configDir := t.TempDir()
	plans := filepath.Join(configDir, "plans")
	if err := os.Mkdir(plans, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(plans, "review.md")
	if err := os.WriteFile(path, []byte("One sentence implementation plan."), 0600); err != nil {
		t.Fatal(err)
	}
	for _, decision := range []string{"allow", "deny"} {
		t.Run(decision, func(t *testing.T) {
			stdin := &fakeStdinWithHook{}
			client := &hermesClient{cfg: Config{Env: map[string]string{"CODEBUDDY_CONFIG_DIR": configDir}}, stdin: stdin}
			broker := newInteractionBroker("", context.Background(), func(_ context.Context, prompt protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
				if prompt.Kind != "plan" || !strings.Contains(prompt.Title, "One sentence implementation plan.") {
					t.Errorf("native plan missing from approval: %+v", prompt)
				}
				return protocol.InteractionAnswer{Decision: decision}, nil
			}, nil)
			defer broker.stop()
			raw, _ := json.Marshal(map[string]any{"sessionId": "session", "toolCall": map[string]any{"rawInput": map[string]any{}, "_meta": map[string]any{"codebuddy.ai/toolName": "ExitPlanMode", "codebuddy.ai/planFilePath": path, "futureMetadata": true}}, "options": []map[string]string{{"kind": "allow_always", "optionId": "forever"}, {"kind": "reject_once", "optionId": "reject_and_exit_plan"}, {"kind": "allow_once", "optionId": "allow"}, {"kind": "reject_once", "optionId": "reject"}}})
			handleCodebuddyACPInteraction(client, broker, "session", "session/request_permission", json.RawMessage(`1`), raw)
			broker.wg.Wait()
			var response struct {
				Result struct {
					Outcome struct {
						OptionID string `json:"optionId"`
					} `json:"outcome"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(stdin.Lines()[0]), &response); err != nil {
				t.Fatal(err)
			}
			want := "allow"
			if decision == "deny" {
				want = "reject"
			}
			if response.Result.Outcome.OptionID != want {
				t.Fatalf("native plan response changed intent: %+v", response)
			}
		})
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("private content"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodebuddyApprovalPlan(Config{Env: map[string]string{"CODEBUDDY_CONFIG_DIR": configDir}}, outside); err == nil {
		t.Fatal("plan escaped the account directory")
	}
	link := filepath.Join(plans, "external.md")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := readCodebuddyApprovalPlan(Config{Env: map[string]string{"CODEBUDDY_CONFIG_DIR": configDir}}, link); err == nil {
			t.Fatal("plan symlink escaped the account directory")
		}
	}
}
