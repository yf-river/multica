package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDshSuspendedInteractionContract(t *testing.T) {
	for _, kind := range []string{"plan", "question"} {
		t.Run(kind, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "dsh-runtime", "fixtures", "waiting-"+kind+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var frames []dshFrame
			for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
				var frame dshFrame
				if err := json.Unmarshal(line, &frame); err != nil {
					t.Fatal(err)
				}
				frames = append(frames, frame)
			}
			run := func(input []dshFrame) *dshRunState {
				state := &dshRunState{usage: make(map[string]TokenUsage)}
				for _, frame := range input {
					handleDshFrame(frame, "contract-task", make(chan Message, 8), state)
				}
				return state
			}
			state := run(frames)
			if state.result == nil || state.result.PendingInteraction == nil || state.result.SessionID != "contract-session" || state.result.PendingInteraction.Kind != kind {
				t.Fatalf("lost suspended interaction: %#v", state.result)
			}
			if kind == "plan" && state.result.PendingInteraction.PlanMarkdown != "# A plan\n\nRead, then write after approval.\n" {
				t.Fatal("plan Markdown changed")
			}
			for _, failure := range []string{"missing", "different-session", "duplicate", "wrong-status", "completed"} {
				t.Run(failure, func(t *testing.T) {
					modified := append([]dshFrame(nil), frames...)
					switch failure {
					case "missing":
						modified = append(modified[:2], modified[3:]...)
					case "different-session":
						modified[2].SessionID = "other-session"
					case "duplicate":
						modified = append(modified[:3], append([]dshFrame{modified[2]}, modified[3:]...)...)
					case "wrong-status":
						modified[len(modified)-1].Status = "waiting_plan"
						if kind == "plan" {
							modified[len(modified)-1].Status = "waiting_input"
						}
					case "completed":
						modified[len(modified)-1].Status = "completed"
					}
					got := run(modified)
					if got.result == nil || got.result.Status != "failed" || got.result.PendingInteraction != nil {
						t.Fatalf("unsafe interaction accepted: %#v", got.result)
					}
				})
			}
		})
	}
}

func TestDshInteractionFlagReachesSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	t.Setenv("MULTICA_DSH_INTERACTIONS", "1")
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			bin := writeDshFixture(t, `
IFS= read -r command
if [ "$MULTICA_DSH_INTERACTIONS" = "1" ]; then
 case "$command" in *'"human_interactions":true'*) ;; *) exit 8 ;; esac
 printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","capabilities":{"plan_review":true,"user_questions":true}}'
 printf '%s\n' '{"v":1,"type":"session","request_id":"flag-task","session_id":"native-session"}'
 printf '%s\n' '{"v":1,"type":"plan_review","request_id":"flag-task","session_id":"native-session","interaction":{"kind":"plan","call_id":"native-call","title":"Plan","questions":[],"plan_markdown":"# Wait"}}'
 printf '%s\n' '{"v":1,"type":"result","request_id":"flag-task","session_id":"native-session","status":"waiting_plan"}'
elif [ "$MULTICA_DSH_INTERACTIONS" = "0" ]; then
 case "$command" in *'"human_interactions":true'*) exit 9 ;; esac
 printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","capabilities":{}}'
 printf '%s\n' '{"v":1,"type":"result","request_id":"flag-task","status":"completed"}'
else exit 10
fi
`)
			configured := map[string]string{"MULTICA_DSH_INTERACTIONS": "1"}
			backend, err := New("dsh", Config{ExecutablePath: bin, TaskID: "flag-task", Env: configured})
			if err != nil {
				t.Fatal(err)
			}
			session, err := backend.Execute(context.Background(), "plan", ExecOptions{Cwd: t.TempDir(), HumanInteractions: enabled, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			result := <-session.Result
			if enabled && (result.Status != "waiting_plan" || result.PendingInteraction == nil) {
				t.Fatalf("interaction flag lost: %#v", result)
			}
			if !enabled && result.Status != "completed" {
				t.Fatalf("disabled flag inherited: %#v", result)
			}
			if configured["MULTICA_DSH_INTERACTIONS"] != "1" {
				t.Fatal("shared backend configuration mutated")
			}
		})
	}
}
