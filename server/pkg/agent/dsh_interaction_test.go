package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
