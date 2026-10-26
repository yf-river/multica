package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDSHSuspendedReportSurvivesRestart(t *testing.T) {
	cfg := Config{WorkspacesRoot: t.TempDir(), ServerBaseURL: "https://api.example.test", Profile: "isolated", DaemonID: "test-daemon"}
	pending := &protocol.SuspendedInteraction{CallID: "plan-call", InteractionPrompt: protocol.InteractionPrompt{Kind: "plan", Title: "Plan", PlanMarkdown: "# Plan\n\nWait for approval."}}
	report := terminalTaskReport{kind: terminalTaskReportSuspend, taskID: "task-plan", output: "Plan", sessionID: "native-session", workDir: "/isolated/work", interaction: pending}
	store := newTerminalReportStore(cfg)
	if err := store.enqueue(report); err != nil {
		t.Fatal(err)
	}
	restored := newTerminalReportStore(cfg)
	items, err := restored.list()
	if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0].report, report) {
		t.Fatalf("suspended report lost on restart: %+v %v", items, err)
	}
	var got struct {
		Output      string                        `json:"output"`
		SessionID   string                        `json:"session_id"`
		WorkDir     string                        `json:"work_dir"`
		Interaction protocol.SuspendedInteraction `json:"interaction"`
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/api/daemon/tasks/task-plan/suspend" || r.Method != http.MethodPost {
			t.Errorf("wrong suspension request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := NewClient(server.URL)
	if err := client.suspendTaskWithRetrySchedule(context.Background(), items[0].report, []time.Duration{}); err != nil {
		t.Fatal(err)
	}
	if !called || got.SessionID != report.sessionID || got.WorkDir != report.workDir || !reflect.DeepEqual(got.Interaction, *pending) {
		t.Fatalf("replay changed native context: %+v", got)
	}
}
