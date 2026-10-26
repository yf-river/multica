package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCodexNativeQuestionBridge(t *testing.T) {
	c, _, _ := newTestCodexClient(t)
	c.threadID = "root"
	c.setActiveTurnID("turn")
	stdin := &fakeStdinWithHook{}
	c.stdin = stdin
	c.interactions = newInteractionBroker("", context.Background(), func(ctx context.Context, prompt protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
		if prompt.Kind != "question" || len(prompt.Questions) != 1 || prompt.Questions[0].ID != "scope" {
			t.Errorf("incorrect question: %+v", prompt)
		}
		return protocol.InteractionAnswer{Decision: "allow", Answers: map[string][]string{"scope": {"chosen"}}}, nil
	}, nil)
	defer c.interactions.stop()
	c.handleLine(`{"jsonrpc":"2.0","id":"native-id","method":"item/tool/requestUserInput","params":{"threadId":"root","turnId":"turn","questions":[{"id":"scope","question":"Which scope?"}]}}`)
	c.interactions.wg.Wait()
	lines := stdin.Lines()
	if len(lines) != 1 {
		t.Fatalf("native responses: %v", lines)
	}
	var response struct {
		ID     string `json:"id"`
		Result struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "native-id" || response.Result.Answers["scope"].Answers[0] != "chosen" {
		t.Fatalf("native response changed: %+v", response)
	}
}

func TestRuntimeInteractionCancellationDoesNotBlockReader(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	broker := newInteractionBroker("", context.Background(), func(ctx context.Context, prompt protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
		close(started)
		<-ctx.Done()
		return protocol.InteractionAnswer{}, ctx.Err()
	}, nil)
	broker.start("id", protocol.InteractionPrompt{Kind: "approval", Title: "Confirm"}, func(answer protocol.InteractionAnswer, err error) {
		if err == nil {
			t.Error("cancelled request succeeded")
		}
		close(finished)
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request never started")
	}
	if !broker.waiting() {
		t.Fatal("waiting state missing")
	}
	broker.resolve("id")
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("request cancellation blocked")
	}
	broker.stop()
	if broker.waiting() {
		t.Fatal("waiting state leaked after cancellation")
	}
}

func TestCodebuddyQuestionReturnsOriginalQuestionKey(t *testing.T) {
	stdin := &fakeStdinWithHook{}
	broker := newInteractionBroker("", context.Background(), func(ctx context.Context, prompt protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
		return protocol.InteractionAnswer{Decision: "allow", Answers: map[string][]string{"0": {"A", "B"}}}, nil
	}, nil)
	defer broker.stop()
	msg := codebuddySDKMessage{RequestID: "request", Request: json.RawMessage(`{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"header":"Scope","question":"Which scope?","multiSelect":true,"options":[{"label":"A"},{"label":"B"}]}]}}`)}
	if !handleCodebuddyInteraction(msg, stdin, broker) {
		t.Fatal("question was not handled")
	}
	broker.wg.Wait()
	var response struct {
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Allowed bool `json:"allowed"`
				Updated struct {
					Answers map[string]string `json:"answers"`
				} `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(stdin.Lines()[0]), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Response.Response.Allowed || response.Response.RequestID != "request" || response.Response.Response.Updated.Answers["Which scope?"] != "A, B" {
		t.Fatalf("incorrect CodeBuddy answer: %+v", response)
	}
}

func TestCodexSecretQuestionDoesNotEnterPersistentInteraction(t *testing.T) {
	_, err := codexQuestionPrompt(json.RawMessage(`{"questions":[{"id":"key","question":"API key?","isSecret":true}]}`))
	if err == nil {
		t.Fatal("secret input must use credential workflow")
	}
}
