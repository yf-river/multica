package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestTaskInteractionRequesterPersistsThenPollsAnswer(t *testing.T) {
	var polls atomic.Int32
	var expired atomic.Bool
	var id string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Error("interaction lease must use POST")
		}
		row := protocol.TaskInteraction{TaskID: "task", Status: "pending", ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
		switch {
		case strings.HasSuffix(r.URL.Path, "/interactions"):
			var req struct {
				ID       string `json:"id"`
				Provider string `json:"provider"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Provider != "codex" {
				t.Error("provider lost")
			}
			id = req.ID
		case strings.HasSuffix(r.URL.Path, "/lease"):
			polls.Add(1)
			row.Status = "responded"
			row.Response = &protocol.InteractionAnswer{Decision: "allow"}
		case strings.HasSuffix(r.URL.Path, "/expire"):
			expired.Store(true)
		}
		row.ID = id
		json.NewEncoder(w).Encode(row)
	}))
	defer srv.Close()
	client := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	answer, err := client.taskInteractionRequester("task", "codex")(ctx, protocol.InteractionPrompt{Kind: "approval", Title: "Execute?"})
	if err != nil || answer.Decision != "allow" || polls.Load() != 1 || expired.Load() {
		t.Fatalf("answer=%+v err=%v polls=%d expired=%v", answer, err, polls.Load(), expired.Load())
	}
}

func TestTaskInteractionRequesterRejectsWrongTaskAndExpiresOnFailure(t *testing.T) {
	var expired atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/expire") {
			expired.Store(true)
			json.NewEncoder(w).Encode(map[string]string{"status": "expired"})
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(protocol.TaskInteraction{ID: req.ID, TaskID: "other-task", Status: "responded", Response: &protocol.InteractionAnswer{Decision: "allow"}, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)})
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL).taskInteractionRequester("task", "codex")(context.Background(), protocol.InteractionPrompt{Kind: "approval", Title: "Execute?"})
	if err == nil || !expired.Load() {
		t.Fatalf("wrong-task response accepted or cleanup missing: %v", err)
	}
}

func TestIdleWatchdogWaitsForHumanInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waiting atomic.Bool
	waiting.Store(true)
	var last atomic.Int64
	last.Store(time.Now().Add(-time.Minute).UnixNano())
	var inFlight atomic.Int32
	var fired atomic.Bool
	var threshold atomic.Int64
	done := make(chan struct{})
	d := &Daemon{}
	go func() {
		defer close(done)
		d.runIdleWatchdog(ctx, 20*time.Millisecond, 0, &last, &inFlight, &fired, &threshold, cancel, make(chan agent.Message), slog.Default(), waiting.Load)
	}()
	select {
	case <-done:
		t.Fatal("waiting for human input was treated as inactivity")
	case <-time.After(70 * time.Millisecond):
	}
	waiting.Store(false)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchdog failed to resume after waiting ended")
	}
	if !fired.Load() {
		t.Fatal("watchdog ended without observing inactivity")
	}
}
