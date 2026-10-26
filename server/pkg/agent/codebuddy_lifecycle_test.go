package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCodebuddyChildResultCannotCompleteOrReplaceRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture")
	}
	path := filepath.Join(t.TempDir(), "codebuddy")
	script := `#!/bin/bash
IFS= read -r _
printf '%s\n' '{"type":"system","session_id":"root"}'
printf '%s\n' '{"type":"assistant","session_id":"root","_requestId":"root-request","message":{"content":[{"type":"tool_use","id":"task-call","name":"Task","input":{}}]}}'
printf '%s\n' '{"type":"assistant","parent_tool_use_id":"task-call","session_id":"child","_requestId":"child-request","message":{"content":[{"type":"text","text":"child answer"}]}}'
printf '%s\n' '{"type":"result","session_id":"root","_requestId":"root-request","result":"root answer","is_error":false}'
if IFS= read -r -t 0.05 _; then exit 3; else code=$?; fi
if [ "$code" -eq 1 ]; then exit 4; fi
printf '%s\n' '{"type":"result","parent_tool_use_id":"task-call","session_id":"child","_requestId":"child-request","result":"child answer","is_error":true}'
printf '%s\n' '{"type":"user","session_id":"root","message":{"content":[{"type":"tool_result","tool_use_id":"task-call","content":"child answer"}]}}'
`
	writeTestExecutable(t, path, []byte(script))
	b := &codebuddyBackend{cfg: Config{ExecutablePath: path, Logger: slog.Default(), Env: map[string]string{"CODEBUDDY_CONFIG_DIR": t.TempDir()}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := b.Execute(ctx, "run", ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	childEnded := false
	for msg := range session.Messages {
		if msg.Type == MessageRuntime && strings.Contains(msg.Content, `"kind":"subagent"`) && strings.Contains(msg.Content, `"phase":"end"`) {
			childEnded = true
		}
	}
	result := <-session.Result
	if result.Status != "completed" || result.SessionID != "root" || result.ProviderTraceID != "root-request" || result.Output != "root answer" || !childEnded {
		t.Fatalf("root or child completion lost: %+v childEnded=%v", result, childEnded)
	}
}

func TestCodebuddyAssistantBackpressurePreservesLongOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Message, 2)
	done := make(chan struct{})
	b := &codebuddyBackend{}
	go func() {
		defer close(done)
		defer close(ch)
		for i := 0; i < 700; i++ {
			raw, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": fmt.Sprintf("line-%d", i)}}})
			b.handleAssistant(ctx, codebuddySDKMessage{Message: raw}, ch, map[string]TokenUsage{}, map[codebuddyUsageCall]TokenUsage{})
		}
	}()
	time.Sleep(20 * time.Millisecond)
	count := 0
	for msg := range ch {
		if msg.Content != fmt.Sprintf("line-%d", count) {
			t.Fatalf("message %d missing: %+v", count, msg)
		}
		count++
	}
	<-done
	if count != 700 {
		t.Fatalf("got %d messages", count)
	}
	blocked := make(chan struct{})
	full := make(chan Message)
	go func() { defer close(blocked); sendCodebuddyMessage(ctx, full, Message{Type: MessageText}) }()
	cancel()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("cancel did not release backpressure")
	}
}

func TestCodebuddyLateChildOutputDoesNotReopenCompletedCall(t *testing.T) {
	var events []Message
	lifecycle := newCodebuddyLifecycle(func(msg Message) { events = append(events, msg) })
	lifecycle.observe(codebuddySDKMessage{Type: "assistant", SessionID: "root", Message: json.RawMessage(`{"content":[{"type":"tool_use","id":"task","name":"Task"}]}`)})
	lifecycle.observe(codebuddySDKMessage{Type: "user", SessionID: "root", Message: json.RawMessage(`{"content":[{"type":"tool_result","tool_use_id":"task","content":"done"}]}`)})
	if lifecycle.observe(codebuddySDKMessage{Type: "assistant", ParentToolUseID: "task", SessionID: "child", Message: json.RawMessage(`{"content":[{"type":"text","text":"late child text"}]}`)}) {
		t.Fatal("late child attributed to root")
	}
	if len(lifecycle.children) != 0 {
		t.Fatal("late output revived a completed child")
	}
	found := false
	for _, event := range events {
		if strings.Contains(event.Content, `"phase":"attached"`) && strings.Contains(event.Content, `"session_id":"child"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("child provenance lost")
	}
}
