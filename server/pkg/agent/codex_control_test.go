package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
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

// 第一回合在 Goal RPC 前完成时，仍须等原生续跑的完成事件。
func TestCodexGoalWaitsForNativeContinuation(t *testing.T) {
	fake := writeFakeCodexAppServer(t, `read line
echo '{"id":1,"result":{}}'
read line
read line
echo '{"id":2,"result":{"thread":{"id":"goal-thread"}}}'
read line
case "$line" in *'"method":"turn/start"'*) ;; *) exit 11;; esac
echo '{"id":3,"result":{}}'
echo '{"method":"turn/started","params":{"threadId":"goal-thread","turn":{"id":"first"}}}'
echo '{"method":"item/completed","params":{"threadId":"goal-thread","item":{"type":"agentMessage","id":"a","text":"first answer","phase":"final_answer"}}}'
echo '{"method":"turn/completed","params":{"threadId":"goal-thread","turn":{"id":"first","status":"completed"}}}'
read line
case "$line" in *'"method":"thread/goal/get"'*) ;; *) exit 12;; esac
echo '{"id":4,"result":{"goal":null}}'
read line
case "$line" in *'"method":"thread/goal/set"'*) ;; *) exit 13;; esac
echo '{"id":5,"result":{"goal":{"status":"active","objective":"finish"}}}'
echo '{"method":"thread/goal/updated","params":{"threadId":"goal-thread","goal":{"status":"active"}}}'
sleep 0.1
echo '{"method":"turn/started","params":{"threadId":"goal-thread","turn":{"id":"second"}}}'
echo '{"method":"item/completed","params":{"threadId":"goal-thread","item":{"type":"agentMessage","id":"b","text":"goal completed","phase":"final_answer"}}}'
echo '{"method":"thread/goal/updated","params":{"threadId":"goal-thread","goal":{"status":"complete"}}}'
echo '{"method":"turn/completed","params":{"threadId":"goal-thread","turn":{"id":"second","status":"completed"}}}'
while read line; do exit 14; done
`)
	result := executeFakeCodex(t, fake, ExecOptions{Command: "/goal finish", Timeout: 5 * time.Second})
	if result.Status != "completed" || result.Output != "goal completed" {
		t.Fatalf("Goal task ended at wrong boundary: %+v", result)
	}
}

func TestCodexGoalPausePrecedesTurnCancellation(t *testing.T) {
	c, _, _ := newTestCodexClient(t)
	c.setThreadID("goal-thread")
	c.setActiveTurnID("goal-turn")
	c.goalActive.Store(true)
	done := make(chan bool, 1)
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
		if request.Method == "thread/goal/set" && (request.Params["status"] != "paused" || request.Params["origin"] != "automatic") {
			t.Fatal("cancellation used incorrect goal state or provenance")
		}
		c.handleLine(fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
		if request.Method == "turn/interrupt" {
			done <- true
		}
	}
	c.stdin = stdin
	if !interruptCodexTurn(c, "goal-thread", done, time.Second, nil) {
		t.Fatal("native cancellation did not complete")
	}
	if len(methods) != 2 || methods[0] != "thread/goal/set" || methods[1] != "turn/interrupt" || c.goalActive.Load() {
		t.Fatalf("goal cancellation order: %v", methods)
	}
}

func TestCodexGoalStopReasonsAreNotSuccess(t *testing.T) {
	for _, status := range []string{"blocked", "budgetLimited", "usageLimited", "complete", "paused"} {
		t.Run(status, func(t *testing.T) {
			c, _, _ := newTestCodexClient(t)
			c.observeGoalState("thread/goal/updated", map[string]any{"goal": map[string]any{"status": status}})
			if c.goalStopError() != "" {
				t.Fatal("historical goal failure contaminated ordinary input")
			}
			c.observeGoalState("thread/goal/updated", map[string]any{"goal": map[string]any{"status": "active"}})
			c.observeGoalState("thread/goal/updated", map[string]any{"goal": map[string]any{"status": status}})
			wantError := status == "blocked" || status == "budgetLimited" || status == "usageLimited"
			if (c.goalStopError() != "") != wantError {
				t.Fatalf("incorrect stop result for %s", status)
			}
		})
	}
}
