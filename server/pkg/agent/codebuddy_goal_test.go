package agent

import (
	"context"
	"encoding/json"
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
