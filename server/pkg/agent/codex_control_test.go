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
				if request.Method == "thread/goal/clear" {
					c.handleLine(fmt.Sprintf(`{"id":%d,"result":{"cleared":true}}`, request.ID))
					c.handleLine(`{"method":"thread/goal/cleared","params":{"threadId":"current"}}`)
				} else {
					status, objective := "active", "existing objective"
					if value, ok := request.Params["status"].(string); ok {
						status = value
					}
					if value, ok := request.Params["objective"].(string); ok {
						objective = value
					}
					c.handleLine(fmt.Sprintf(`{"id":%d,"result":{"goal":{"objective":%q,"status":%q,"tokensUsed":123}}}`, request.ID, objective, status))
					if request.Method == "thread/goal/set" {
						c.handleLine(fmt.Sprintf(`{"method":"thread/goal/updated","params":{"threadId":"current","goal":{"objective":%q,"status":%q}}}`, objective, status))
					}
				}
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
echo '{"method":"thread/goal/updated","params":{"threadId":"goal-thread","goal":{"status":"active","objective":"finish"}}}'
sleep 0.1
echo '{"method":"turn/started","params":{"threadId":"goal-thread","turn":{"id":"second"}}}'
echo '{"method":"item/completed","params":{"threadId":"goal-thread","item":{"type":"agentMessage","id":"b","text":"goal completed","phase":"final_answer"}}}'
echo '{"method":"thread/goal/updated","params":{"threadId":"goal-thread","goal":{"status":"complete","objective":"finish"}}}'
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
		if request.Method == "thread/goal/set" {
			c.handleLine(fmt.Sprintf(`{"id":%d,"result":{"goal":{"objective":"existing","status":"paused"}}}`, request.ID))
			c.handleLine(`{"method":"thread/goal/updated","params":{"threadId":"goal-thread","goal":{"objective":"existing","status":"paused"}}}`)
		} else {
			c.handleLine(fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
		}
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

func TestCodexPlanRequiresMatchingNativeSettings(t *testing.T) {
	for _, kind := range []string{"confirmed", "ack_only", "wrong_model", "wrong_thread"} {
		t.Run(kind, func(t *testing.T) {
			c, _, _ := newTestCodexClient(t)
			c.setThreadID("plan-thread")
			stdin := &fakeStdinWithHook{}
			stdin.afterWrite = func() {
				lines := stdin.Lines()
				var request struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
				}
				if err := json.Unmarshal([]byte(lines[len(lines)-1]), &request); err != nil {
					t.Fatal(err)
				}
				if request.Method != "thread/settings/update" {
					t.Fatalf("wrong native mode method %s", request.Method)
				}
				c.handleLine(fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
				if kind == "ack_only" {
					return
				}
				thread, model := "plan-thread", "resolved-model"
				if kind == "wrong_thread" {
					thread = "other-thread"
				}
				if kind == "wrong_model" {
					model = "other-model"
				}
				c.handleLine(fmt.Sprintf(`{"method":"thread/settings/updated","params":{"threadId":%q,"threadSettings":{"collaborationMode":{"mode":"default","settings":{"model":%q,"reasoning_effort":"low"}}}}}`, thread, model))
			}
			c.stdin = stdin
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			_, err := c.applyRuntimeCommand(ctx, ExecOptions{Command: "/plan off", Model: "resolved-model", ThinkingLevel: "low"})
			if (err == nil) != (kind == "confirmed") {
				t.Fatalf("plan setting acknowledgement %s: %v", kind, err)
			}
		})
	}
}

func TestCodexResumedGoalAdoptsLiveTurnFromStartResponse(t *testing.T) {
	c, _, _ := newTestCodexClient(t)
	c.setThreadID("resumed-thread")
	gate := &codexTurnNotificationGate{}
	c.acceptNotification = gate.accept
	c.handleLine(`{"method":"turn/started","params":{"threadId":"resumed-thread","turn":{"id":"native-goal-turn"}}}`)
	if c.activeTurnID() != "" {
		t.Fatal("resume-time event crossed the closed gate")
	}
	gate.arm()
	response := json.RawMessage(`{"turn":{"id":"native-goal-turn","status":"inProgress"}}`)
	c.adoptTurnStartResponse(response)
	if c.activeTurnID() != "native-goal-turn" {
		t.Fatal("steered native Goal lost its active turn identity")
	}
	c.handleLine(`{"method":"turn/completed","params":{"threadId":"resumed-thread","turn":{"id":"native-goal-turn","status":"completed"}}}`)
	c.adoptTurnStartResponse(response)
	if c.activeTurnID() != "" || !c.turnCompleted {
		t.Fatal("late turn/start response reopened a completed native turn")
	}
}

func TestCodexLiveGoalKeepsFastNativeLimitState(t *testing.T) {
	c, _, _ := newTestCodexClient(t)
	c.setThreadID("live-thread")
	c.setActiveTurnID("live-turn")
	c.commandReady.Store(true)
	stdin := &fakeStdinWithHook{}
	stdin.afterWrite = func() {
		lines := stdin.Lines()
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		json.Unmarshal([]byte(lines[len(lines)-1]), &request)
		if request.Method == "thread/goal/get" {
			c.handleLine(fmt.Sprintf(`{"id":%d,"result":{"goal":null}}`, request.ID))
			return
		}
		c.handleLine(fmt.Sprintf(`{"id":%d,"result":{"goal":{"objective":"new objective","status":"active"}}}`, request.ID))
		c.handleLine(`{"method":"thread/goal/updated","params":{"threadId":"live-thread","goal":{"objective":"new objective","status":"active"}}}`)
		c.handleLine(`{"method":"thread/goal/updated","params":{"threadId":"live-thread","goal":{"objective":"new objective","status":"usageLimited"}}}`)
	}
	c.stdin = stdin
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.runLiveCommand(ctx, ctx, "/goal new objective", ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if c.goalActive.Load() || c.goalStopError() != "codex native goal stopped: usageLimited" {
		t.Fatal("control reply overwrote the newer native limit state")
	}
}
