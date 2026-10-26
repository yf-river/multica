package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRuntimeInputLiveCommandReadinessAndDetachedReceipt(t *testing.T) {
	for _, outcome := range []string{"confirmed", "uncertain"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var requests, calls, receipts atomic.Int32
			var ready atomic.Bool
			claim := protocol.RuntimeInputClaim{RuntimeID: "runtime", DispatchedAt: "2026-10-26T00:00:00Z"}
			d := taskSupplementTestDaemon(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if strings.HasSuffix(r.URL.Path, "/claim") {
					var req struct {
						protocol.RuntimeInputClaim
						ReadyKinds []string `json:"ready_kinds"`
					}
					if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.ReadyKinds) != 1 || req.ReadyKinds[0] != "command" || req.RuntimeInputClaim != claim {
						t.Error("claim included an unready input kind or changed the execution identity")
					}
					_ = json.NewEncoder(w).Encode(protocol.TaskRuntimeInput{ID: "input", TaskID: "task", Kind: "command", Content: "/goal pause", Status: "delivering"})
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/input/ack") {
					t.Error("unexpected runtime input path")
				}
				var ack protocol.RuntimeInputAck
				if json.NewDecoder(r.Body).Decode(&ack) != nil || ack.RuntimeInputClaim != claim {
					t.Error("ack lost its execution identity")
				}
				if outcome == "confirmed" {
					if ack.Status != "delivered" || ack.Output != "已暂停" || ack.FailureReason != "" {
						t.Errorf("native confirmation lost: %+v", ack)
					}
				} else if ack.Status != "failed" || ack.FailureReason != "delivery_unknown" {
					t.Errorf("transport error was incorrectly reported as certain: %+v", ack)
				}
				if receipts.Add(1) < 3 {
					w.WriteHeader(503)
					return
				}
				w.WriteHeader(200)
			})
			session := &agent.Session{
				CommandReady: ready.Load,
				RunCommand: func(_ context.Context, command string) (string, error) {
					calls.Add(1)
					if command != "/goal pause" {
						t.Error("typed native command was wrapped as a model prompt")
					}
					cancel()
					if outcome == "uncertain" {
						return "", context.DeadlineExceeded
					}
					return "已暂停", nil
				},
				SupplementReady: func() bool { return false },
				Supplement: func(context.Context, string) error {
					t.Error("native command reached text injection")
					return nil
				},
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				d.runTaskRuntimeInputLoop(ctx, session, "task", claim, []string{"command", "text"}, d.logger)
			}()
			time.Sleep(20 * time.Millisecond)
			if requests.Load() != 0 {
				t.Error("claimed before native controls were ready")
			}
			ready.Store(true)
			<-done
			if calls.Load() != 1 || receipts.Load() != 3 {
				t.Fatalf("receipt retry replayed an input or lost acknowledgement: calls=%d receipts=%d", calls.Load(), receipts.Load())
			}
		})
	}
}

func TestRuntimeInputClaimRejectsMalformedOrForeignResponse(t *testing.T) {
	for _, body := range []string{`{`, `{}`, `{"id":"x","task_id":"foreign","kind":"command","status":"delivering","content":"/goal pause"}`, `{"id":"x","task_id":"task","kind":"command","status":"delivering","content":"/unknown"}`} {
		d := taskSupplementTestDaemon(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
		input, err := d.client.ClaimTaskRuntimeInput(t.Context(), "task", protocol.RuntimeInputClaim{}, []string{"command"})
		if input != nil || !errors.Is(err, errInvalidResponseBody) {
			t.Fatalf("malformed claim accepted: input=%+v err=%v", input, err)
		}
	}
	d := taskSupplementTestDaemon(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	if input, err := d.client.ClaimTaskRuntimeInput(t.Context(), "task", protocol.RuntimeInputClaim{}, []string{"command"}); input != nil || err != nil {
		t.Fatalf("empty queue not recognized: input=%+v err=%v", input, err)
	}
}
