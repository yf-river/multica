package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCodebuddySDKInitializationRequiresMatchingAcknowledgement(t *testing.T) {
	for _, subtype := range []string{"success", "error"} {
		t.Run(subtype, func(t *testing.T) {
			stdin := &fakeStdinWithHook{}
			control := newCodebuddyControlClient(stdin)
			stdin.afterWrite = func() {
				var sent struct {
					RequestID string `json:"request_id"`
					Request   struct {
						Subtype      string          `json:"subtype"`
						HasPrompt    bool            `json:"hasPrompt"`
						Capabilities map[string]bool `json:"capabilities"`
					} `json:"request"`
				}
				if err := json.Unmarshal([]byte(stdin.Lines()[0]), &sent); err != nil {
					t.Error(err)
					return
				}
				if sent.Request.Subtype != "initialize" || !sent.Request.HasPrompt || !sent.Request.Capabilities["askUserQuestion"] {
					t.Errorf("missing SDK question capability: %+v", sent)
				}
				control.respond(json.RawMessage(`{"subtype":"success","request_id":"unrelated"}`))
				response, _ := json.Marshal(map[string]string{"subtype": subtype, "request_id": sent.RequestID, "error": "test rejection"})
				control.respond(response)
			}
			if err := control.initialize(t.Context()); (err != nil) != (subtype == "error") {
				t.Fatalf("initialize=%v subtype=%s", err, subtype)
			}
			if control.ready() || len(control.pending) != 0 {
				t.Fatal("initialization enabled steer or retained a pending request")
			}
		})
	}
}

func TestCodebuddySDKInitializationStopsWaiting(t *testing.T) {
	for _, closeProcess := range []bool{false, true} {
		stdin := &fakeStdinWithHook{}
		control := newCodebuddyControlClient(stdin)
		ctx, cancel := context.WithCancel(t.Context())
		stdin.afterWrite = func() {
			if closeProcess {
				control.close()
			} else {
				cancel()
			}
		}
		start := time.Now()
		if err := control.initialize(ctx); err == nil {
			t.Fatal("missing initialization acknowledgement succeeded")
		}
		cancel()
		if time.Since(start) > time.Second || len(control.pending) != 0 {
			t.Fatal("initialization did not release its pending request")
		}
	}
}

func TestCodebuddySDKInitializationDoesNotAcceptCancelledAcknowledgement(t *testing.T) {
	stdin := &fakeStdinWithHook{}
	control := newCodebuddyControlClient(stdin)
	ctx, cancel := context.WithCancel(t.Context())
	stdin.afterWrite = func() {
		var request struct {
			RequestID string `json:"request_id"`
		}
		_ = json.Unmarshal([]byte(stdin.Lines()[0]), &request)
		response, _ := json.Marshal(map[string]string{"subtype": "success", "request_id": request.RequestID})
		control.respond(response)
		cancel()
	}
	if err := control.initialize(ctx); err != context.Canceled {
		t.Fatalf("cancelled initialization accepted acknowledgement: %v", err)
	}
	if len(control.pending) != 0 {
		t.Fatal("cancelled initialization retained pending request")
	}
	stdin = &fakeStdinWithHook{}
	control = newCodebuddyControlClient(stdin)
	if err := control.initialize(ctx); err != context.Canceled || len(stdin.Lines()) != 0 {
		t.Fatal("cancelled initialization wrote another request")
	}
}

func TestCodebuddySteerAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name            string
		accepted        bool
		responseSession string
		wantError       bool
	}{
		{"accepted", true, "session", false},
		{"stale turn", false, "session", true},
		{"different session", true, "other", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdin := &fakeStdinWithHook{}
			control := newCodebuddyControlClient(stdin)
			control.observe(codebuddySDKMessage{Type: "assistant", SessionID: "session", ProviderRequestID: "request"})
			stdin.afterWrite = func() {
				var sent struct {
					RequestID string `json:"request_id"`
					Request   struct {
						Subtype           string              `json:"subtype"`
						SessionID         string              `json:"session_id"`
						ExpectedRequestID string              `json:"expected_request_id"`
						ContentBlocks     []map[string]string `json:"content_blocks"`
					} `json:"request"`
				}
				lines := stdin.Lines()
				if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sent); err != nil {
					t.Fatal(err)
				}
				if sent.Request.Subtype != "steer" || sent.Request.SessionID != "session" || sent.Request.ExpectedRequestID != "request" || len(sent.Request.ContentBlocks) != 1 || sent.Request.ContentBlocks[0]["text"] != "extra requirement" {
					t.Fatalf("incorrect steer target or content: %+v", sent)
				}
				data, _ := json.Marshal(map[string]any{"subtype": "success", "request_id": sent.RequestID, "response": map[string]any{"steered": test.accepted, "session_id": test.responseSession}})
				control.respond(data)
			}
			if err := control.supplement(context.Background(), "extra requirement"); (err != nil) != test.wantError {
				t.Fatalf("supplement error = %v", err)
			}
		})
	}
}

func TestCodebuddySteerOnlyDuringRootExecution(t *testing.T) {
	stdin := &fakeStdinWithHook{}
	control := newCodebuddyControlClient(stdin)
	for _, msg := range []codebuddySDKMessage{
		{Type: "system", SessionID: "root"},
		{Type: "assistant", ParentToolUseID: "child", SessionID: "child", ProviderRequestID: "child-request"},
	} {
		control.observe(msg)
		if control.ready() {
			t.Fatal("initialization or child execution enabled root steer")
		}
	}
	control.observe(codebuddySDKMessage{Type: "assistant", SessionID: "root", ProviderRequestID: "root-request"})
	if !control.ready() {
		t.Fatal("root turn not ready")
	}
	control.observe(codebuddySDKMessage{Type: "result", SessionID: "child", ParentToolUseID: "child-tool"})
	if !control.ready() {
		t.Fatal("child result closed root steer")
	}
	control.observe(codebuddySDKMessage{Type: "result"})
	if err := control.supplement(context.Background(), "late"); err == nil {
		t.Fatal("completed turn accepted additional input")
	}
	if len(stdin.Lines()) != 0 {
		t.Fatal("late steer wrote to stdin")
	}
	control.close()
	control.close()
	control.observe(codebuddySDKMessage{Type: "assistant", SessionID: "root", ProviderRequestID: "another"})
	if control.ready() {
		t.Fatal("closed execution became active again")
	}
}

func TestCodebuddySteerCancellationAndExit(t *testing.T) {
	stdin := &fakeStdinWithHook{}
	control := newCodebuddyControlClient(stdin)
	control.observe(codebuddySDKMessage{Type: "assistant", SessionID: "root", ProviderRequestID: "request"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := control.supplement(ctx, "cancelled"); err == nil || len(stdin.Lines()) != 0 {
		t.Fatal("cancelled input was sent")
	}
	stdin.afterWrite = control.close
	if err := control.supplement(context.Background(), "exit"); err == nil {
		t.Fatal("exit without acknowledgement reported success")
	}
}
