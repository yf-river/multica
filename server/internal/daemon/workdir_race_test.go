package daemon

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func newWorkdirTestDaemon(serverURL, workspacesRoot, runtimeID string) *Daemon {
	d := &Daemon{
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		workspaces:         make(map[string]*workspaceState),
		runtimeIndex:       map[string]Runtime{runtimeID: {ID: runtimeID, Provider: "claude"}},
		activeEnvRoots:     make(map[string]int),
		cancelPollInterval: time.Hour,
		cfg:                Config{WorkspacesRoot: workspacesRoot},
	}
	if serverURL != "" {
		d.client = NewClient(serverURL)
	}
	return d
}

func workdirTestTask(id, workspaceID, runtimeID string) Task {
	return Task{
		ID:          id,
		WorkspaceID: workspaceID,
		RuntimeID:   runtimeID,
		IssueID:     "issue-" + id,
		Agent:       &protocol.TaskAgent{Name: "test-agent"},
	}
}

// handleTask must leave StartTask to runTask, after the workdir is prepared.
func TestHandleTask_DoesNotCallStartTaskItself(t *testing.T) {
	t.Parallel()

	var (
		startCalls   atomic.Int64
		runnerCalled atomic.Bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/start"):
			startCalls.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	d := newWorkdirTestDaemon(srv.URL, "", "rt-1")

	// Fake runner that does NOT call StartTask — production runTask does
	// the call itself, after Prepare/Reuse confirms env.WorkDir on disk.
	d.runner = func(_ context.Context, _ Task, _ string, _ int, _ *slog.Logger) (TaskResult, error) {
		runnerCalled.Store(true)
		return TaskResult{Status: "completed"}, nil
	}

	d.handleTask(context.Background(), workdirTestTask("task-no-start", "ws-no-start", "rt-1"), 0)

	if !runnerCalled.Load() {
		t.Fatal("fake runner was never invoked — handleTask aborted before runner, can't assert ordering")
	}
	if got := startCalls.Load(); got != 0 {
		t.Fatalf("handleTask called /start %d time(s); StartTask must be runTask's responsibility now (issue #3999 race A)", got)
	}
}

// The server must not observe a running task before its workdir exists.
func TestRunTask_StartTaskCalledAfterWorkdirOnDisk(t *testing.T) {
	t.Parallel()

	workspacesRoot := t.TempDir()
	workspaceID := "ws-runtask"
	taskID := "task-runtask-after-mkdir"
	expectedEnvRoot := execenv.PredictRootDir(workspacesRoot, workspaceID, taskID)
	expectedWorkDir := filepath.Join(expectedEnvRoot, "workdir")

	var (
		startCalled   atomic.Bool
		workdirOnDisk atomic.Bool
		envRootOnDisk atomic.Bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/start") {
			startCalled.Store(true)
			if info, err := os.Stat(expectedWorkDir); err == nil && info.IsDir() {
				workdirOnDisk.Store(true)
			}
			if info, err := os.Stat(expectedEnvRoot); err == nil && info.IsDir() {
				envRootOnDisk.Store(true)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	missingBin := filepath.Join(t.TempDir(), "definitely-not-claude")
	d := newWorkdirTestDaemon(srv.URL, workspacesRoot, "rt-1")
	d.cfg.Agents = map[string]AgentEntry{"claude": {Path: missingBin}}

	taskLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, _ = d.runTask(context.Background(), workdirTestTask(taskID, workspaceID, "rt-1"), "claude", 0, taskLog)

	if !startCalled.Load() {
		t.Fatal("runTask did not call /start — Fix A's StartTask placement is missing")
	}
	if !envRootOnDisk.Load() {
		t.Fatal("envRoot did not exist on disk when /start was called — Prepare must run before StartTask (issue #3999 race A)")
	}
	if !workdirOnDisk.Load() {
		t.Fatal("envRoot/workdir did not exist on disk when /start was called — os.MkdirAll must complete before StartTask (issue #3999 race A)")
	}
}

func TestRunTaskStartPolicyOnPreparationFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		seedConflict  func(*testing.T, string)
		wantError     string
		wantStartCall bool
	}{
		{
			name: "context refresh fails before start",
			seedConflict: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, ".agent_context"), []byte("blocks managed context directory"), 0o644); err != nil {
					t.Fatalf("seed context path conflict: %v", err)
				}
			},
			wantError: "refresh context files",
		},
		{
			name: "runtime config injection fails after start",
			seedConflict: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(dir, "CLAUDE.md"), 0o755); err != nil {
					t.Fatalf("seed runtime config path conflict: %v", err)
				}
			},
			wantError:     "inject runtime config",
			wantStartCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			priorWorkDir := t.TempDir()
			tt.seedConflict(t, priorWorkDir)

			var startCalled atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/start") {
					startCalled.Store(true)
				}
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(srv.Close)

			d := newWorkdirTestDaemon(srv.URL, t.TempDir(), "rt-1")
			d.cfg.Agents = map[string]AgentEntry{"claude": {Path: filepath.Join(t.TempDir(), "claude")}}
			task := workdirTestTask("task-prepare", "ws-prepare", "rt-1")
			task.PriorWorkDir = priorWorkDir

			_, err := d.runTask(context.Background(), task, "claude", 0, d.logger)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("runTask error = %v, want %q", err, tt.wantError)
			}
			if got := startCalled.Load(); got != tt.wantStartCall {
				t.Fatalf("start called = %v, want %v", got, tt.wantStartCall)
			}
		})
	}
}

// The environment remains GC-protected until completion reporting finishes.
func TestHandleTask_KeepsEnvRootActiveAcrossCompletion(t *testing.T) {
	t.Parallel()

	workspacesRoot := t.TempDir()
	workspaceID := "ws-active-during-complete"
	taskID := "task-active-during-complete"
	expectedEnvRoot := execenv.PredictRootDir(workspacesRoot, workspaceID, taskID)

	var (
		completeCalled   atomic.Bool
		activeAtComplete atomic.Bool
	)

	d := newWorkdirTestDaemon("", workspacesRoot, "rt-1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/complete") {
			completeCalled.Store(true)
			if d.isActiveEnvRoot(expectedEnvRoot) {
				activeAtComplete.Store(true)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	d.client = NewClient(srv.URL)

	d.runner = func(_ context.Context, tk Task, _ string, _ int, _ *slog.Logger) (TaskResult, error) {
		predicted := execenv.PredictRootDir(d.cfg.WorkspacesRoot, tk.WorkspaceID, tk.ID)
		d.markActiveEnvRoot(predicted)
		defer d.unmarkActiveEnvRoot(predicted)
		return TaskResult{
			Status:  "completed",
			EnvRoot: predicted,
		}, nil
	}

	d.handleTask(context.Background(), workdirTestTask(taskID, workspaceID, "rt-1"), 0)

	if !completeCalled.Load() {
		t.Fatal("/complete was never hit — handleTask did not reach reportTaskResult")
	}
	if !activeAtComplete.Load() {
		t.Fatal("env root was NOT in the active set at /complete time — issue #3999 race B regression: GC could reclaim the directory between runner returning and WriteGCMeta landing on disk")
	}
	if d.isActiveEnvRoot(expectedEnvRoot) {
		t.Fatal("env root remained active after handleTask returned — outer guard's deferred unmark did not fire")
	}
}
