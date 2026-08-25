package daemon

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeSetWatcherFanOut(t *testing.T) {
	t.Parallel()

	w := newRuntimeSetWatcher()
	chA, unsubA := w.Subscribe()
	chB, unsubB := w.Subscribe()
	defer unsubA()
	defer unsubB()

	w.notify()
	for _, ch := range []<-chan struct{}{chA, chB} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("expected each subscriber to receive a nudge")
		}
	}

	w.notify()
	w.notify()
	select {
	case <-chA:
	default:
		t.Fatal("expected coalesced nudge to be pending")
	}
	select {
	case <-chA:
		t.Fatal("expected only one coalesced nudge to be queued")
	default:
	}

	select {
	case <-chB:
	default:
	}
	unsubB()
	w.notify()
	select {
	case <-chB:
		t.Fatal("unsubscribed channel must not receive a nudge")
	case <-time.After(50 * time.Millisecond):
	}
}

func newClaimCountingRuntimeDaemon(t *testing.T, pollInterval time.Duration) (*Daemon, *atomic.Int64) {
	t.Helper()
	var claimAttempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/tasks/claim") {
			claimAttempts.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task":null}`))
	}))
	t.Cleanup(srv.Close)

	d := New(Config{
		ServerBaseURL:      srv.URL,
		HeartbeatInterval:  time.Hour,
		PollInterval:       pollInterval,
		MaxConcurrentTasks: 1,
	}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	return d, &claimAttempts
}

func blockRuntimeRequest(r *http.Request, entered chan<- struct{}, release <-chan struct{}) {
	select {
	case entered <- struct{}{}:
	default:
	}
	select {
	case <-release:
	case <-r.Context().Done():
	}
}

func TestRunRuntimePollerIsolatesSlowRuntime(t *testing.T) {
	t.Parallel()

	var fastClaims atomic.Int64
	slowEntered := make(chan struct{}, 1)
	releaseSlow := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/runtimes/runtime-slow/tasks/claim"):
			blockRuntimeRequest(r, slowEntered, releaseSlow)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"task":null}`))
		case strings.HasSuffix(path, "/runtimes/runtime-fast/tasks/claim"):
			fastClaims.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"task":null}`))
		default:
			http.Error(w, "unexpected path: "+path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	defer close(releaseSlow)

	d := New(Config{
		ServerBaseURL:      srv.URL,
		HeartbeatInterval:  time.Hour,
		PollInterval:       50 * time.Millisecond,
		MaxConcurrentTasks: 4,
	}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sem := newTaskSlotSemaphore(d.cfg.MaxConcurrentTasks)
	var taskWG sync.WaitGroup

	slowCtx, slowCancel := context.WithCancel(ctx)
	defer slowCancel()
	go d.runRuntimePoller(slowCtx, ctx, "runtime-slow", sem, make(chan struct{}, 1), &taskWG)

	fastCtx, fastCancel := context.WithCancel(ctx)
	defer fastCancel()
	go d.runRuntimePoller(fastCtx, ctx, "runtime-fast", sem, make(chan struct{}, 1), &taskWG)

	select {
	case <-slowEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("slow runtime claim never entered server handler")
	}

	deadline := time.After(2 * time.Second)
	for fastClaims.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("fast runtime made only %d claims while slow runtime blocked; expected ≥3", fastClaims.Load())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestRunRuntimePollerSkipsClaimWhenAtCapacity(t *testing.T) {
	t.Parallel()

	d, claimAttempts := newClaimCountingRuntimeDaemon(t, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sem := newTaskSlotSemaphore(d.cfg.MaxConcurrentTasks)
	<-sem

	var taskWG sync.WaitGroup
	go d.runRuntimePoller(ctx, ctx, "runtime-busy", sem, make(chan struct{}, 1), &taskWG)

	time.Sleep(200 * time.Millisecond)

	if got := claimAttempts.Load(); got != 0 {
		t.Fatalf("poller called ClaimTask %d times while at capacity; want 0 — pre-claiming risks server-side dispatch_timeout", got)
	}
}

func TestRunRuntimePollerClaimsWhenSlotBecomesAvailable(t *testing.T) {
	t.Parallel()

	d, claimAttempts := newClaimCountingRuntimeDaemon(t, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sem := newTaskSlotSemaphore(d.cfg.MaxConcurrentTasks)
	slot := <-sem

	var taskWG sync.WaitGroup
	wakeup := make(chan struct{}, 1)
	go d.runRuntimePoller(ctx, ctx, "runtime-waiting", sem, wakeup, &taskWG)
	wakeup <- struct{}{}

	time.Sleep(100 * time.Millisecond)
	if got := claimAttempts.Load(); got != 0 {
		t.Fatalf("poller claimed before a slot was available; got %d claims", got)
	}

	sem <- slot

	deadline := time.After(2 * time.Second)
	for claimAttempts.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("poller did not claim after a slot became available")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestPollLoopShutdownWaitsForPollersBeforeTaskWG(t *testing.T) {
	t.Parallel()

	taskID := "00000000-0000-0000-0000-000000000001"
	releaseClaim := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(path, "/tasks/claim"):
			select {
			case <-releaseClaim:
			case <-r.Context().Done():
				_, _ = w.Write([]byte(`{"task":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"task":{"id":"` + taskID + `","runtime_id":"runtime-1","issue_id":"issue-1","agent":{"name":"test"}}}`))
		case strings.HasSuffix(path, "/start"):
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(path, "/fail"):
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(path, "/complete"):
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(path, "/progress"):
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	d := New(Config{
		ServerBaseURL:      srv.URL,
		HeartbeatInterval:  time.Hour,
		PollInterval:       50 * time.Millisecond,
		MaxConcurrentTasks: 1,
	}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	d.workspaces["ws-1"] = &workspaceState{
		workspaceID: "ws-1",
		runtimeIDs:  []string{"runtime-1"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	pollDone := make(chan error, 1)
	go func() {
		pollDone <- d.pollLoop(ctx, nil)
	}()

	// Let the poller enter ClaimTask, then trigger shutdown right as the
	// claim is about to return a task. The race is the window between
	// ClaimTask returning and taskWG.Add(1) executing.
	time.Sleep(100 * time.Millisecond)
	close(releaseClaim)
	cancel()

	select {
	case <-pollDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pollLoop did not return within shutdown deadline")
	}
}

func TestPollLoopTargetsRuntimeWakeup(t *testing.T) {
	t.Parallel()

	var fastClaims atomic.Int64
	var slowClaims atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/runtimes/runtime-fast/tasks/claim"):
			fastClaims.Add(1)
		case strings.HasSuffix(path, "/runtimes/runtime-slow/tasks/claim"):
			slowClaims.Add(1)
		default:
			http.Error(w, "unexpected path: "+path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task":null}`))
	}))
	defer srv.Close()

	d := New(Config{
		ServerBaseURL:      srv.URL,
		HeartbeatInterval:  time.Hour,
		PollInterval:       time.Hour,
		MaxConcurrentTasks: 4,
	}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	d.workspaces["ws-1"] = &workspaceState{
		workspaceID: "ws-1",
		runtimeIDs:  []string{"runtime-fast", "runtime-slow"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskWakeups := make(chan taskWakeup, 1)
	pollDone := make(chan error, 1)
	go func() {
		pollDone <- d.pollLoop(ctx, taskWakeups)
	}()

	taskWakeups <- taskWakeup{}

	deadline := time.After(2 * time.Second)
	for fastClaims.Load() < 1 || slowClaims.Load() < 1 {
		select {
		case <-deadline:
			t.Fatalf("initial poll did not claim both runtimes; fast=%d slow=%d", fastClaims.Load(), slowClaims.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}

	fastClaims.Store(0)
	slowClaims.Store(0)
	taskWakeups <- taskWakeup{runtimeID: "runtime-fast"}

	deadline = time.After(2 * time.Second)
	for fastClaims.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("targeted wakeup did not wake runtime-fast")
		case <-time.After(10 * time.Millisecond):
		}
	}

	time.Sleep(100 * time.Millisecond)
	if got := slowClaims.Load(); got != 0 {
		t.Fatalf("targeted wakeup woke runtime-slow %d times; want 0", got)
	}

	cancel()
	select {
	case <-pollDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pollLoop did not stop")
	}
}

func TestRunRuntimeHeartbeatIsolatesSlowRuntime(t *testing.T) {
	t.Parallel()

	var fastBeats atomic.Int64
	slowEntered := make(chan struct{}, 1)
	releaseSlow := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 1024)
		n, _ := r.Body.Read(body)
		payload := string(body[:n])
		switch {
		case strings.Contains(payload, `"runtime-slow"`):
			blockRuntimeRequest(r, slowEntered, releaseSlow)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case strings.Contains(payload, `"runtime-fast"`):
			fastBeats.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "unexpected payload", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	defer close(releaseSlow)

	d := New(Config{
		ServerBaseURL:     srv.URL,
		HeartbeatInterval: 50 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go d.runRuntimeHeartbeat(ctx, "runtime-slow")
	go d.runRuntimeHeartbeat(ctx, "runtime-fast")

	select {
	case <-slowEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("slow heartbeat never entered server handler")
	}

	deadline := time.After(2 * time.Second)
	for fastBeats.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("fast runtime sent only %d heartbeats while slow runtime blocked; expected ≥3", fastBeats.Load())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// noopWriter discards log output so the test runner doesn't get noisy.
type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }
