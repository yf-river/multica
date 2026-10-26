package agent

import (
	"context"
	"encoding/json"
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
			Outcome struct {
				Outcome string `json:"outcome"`
				Data    struct {
					Answers map[string][]string `json:"answers"`
				} `json:"data"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdin.Lines()[0]), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.Outcome.Outcome != "submitted" || len(response.Result.Outcome.Data.Answers["q_0"]) != 2 {
		t.Fatalf("native answer changed: %+v", response)
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
