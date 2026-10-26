package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestCodexPlanModeUsesCurrentModelAndEffort(t *testing.T) {
	for _, active := range []bool{true, false} {
		params := map[string]any{}
		if err := applyCodexPlanMode(params, ExecOptions{PlanMode: &active, Model: "configured-model", ThinkingLevel: "low"}); err != nil {
			t.Fatal(err)
		}
		mode := params["collaborationMode"].(map[string]any)
		settings := mode["settings"].(map[string]any)
		if settings["model"] != "configured-model" || settings["reasoning_effort"] != "low" || settings["developer_instructions"] != nil {
			t.Fatalf("incorrect settings: %+v", settings)
		}
		if (mode["mode"] == "plan") != active {
			t.Fatalf("incorrect mode: %+v", mode)
		}
	}
	active := true
	if applyCodexPlanMode(map[string]any{}, ExecOptions{PlanMode: &active}) == nil {
		t.Fatal("mode accepted without resolved model")
	}
}

func TestCodexGoalCommandsPreserveNativeAccounting(t *testing.T) {
	for _, test := range []struct {
		command, wantMethod string
		wantWrites          int
	}{
		{"/goal existing objective", "thread/goal/get", 1},
		{"/goal new objective", "thread/goal/set", 2},
		{"/goal pause", "thread/goal/set", 1},
		{"/goal clear", "thread/goal/clear", 1},
	} {
		t.Run(test.command, func(t *testing.T) {
			c, _, _ := newTestCodexClient(t)
			c.threadID = "current"
			stdin := &fakeStdinWithHook{}
			var methods []string
			stdin.afterWrite = func() {
				lines := stdin.Lines()
				var request struct {
					ID     int            `json:"id"`
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				if err := json.Unmarshal([]byte(lines[len(lines)-1]), &request); err != nil {
					t.Fatal(err)
				}
				methods = append(methods, request.Method)
				if request.Params["threadId"] != "current" {
					t.Fatal("goal changed another thread")
				}
				if request.Method == "thread/goal/set" && request.Params["origin"] != "user" {
					t.Fatal("explicit user provenance missing")
				}
				c.handleLine(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"goal":{"objective":"existing objective","status":"active","tokensUsed":123}}}`, request.ID))
			}
			c.stdin = stdin
			if _, err := c.applyRuntimeCommand(context.Background(), ExecOptions{Command: test.command}); err != nil {
				t.Fatal(err)
			}
			if len(methods) != test.wantWrites || methods[len(methods)-1] != test.wantMethod {
				t.Fatalf("unexpected goal RPCs: %v", methods)
			}
		})
	}
}
