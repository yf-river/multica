package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/repocache"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func newHealthTestDaemon(cliVersion string) *Daemon {
	return &Daemon{
		cfg:        Config{CLIVersion: cliVersion},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
}

func TestHealthHandlerReportsCLIVersionAndActiveTaskCount(t *testing.T) {
	t.Parallel()

	d := newHealthTestDaemon("v9.9.9")
	d.cfg.DaemonID = "daemon-test"
	d.cfg.DeviceName = "dev"
	d.cfg.ServerBaseURL = "http://localhost:8080"
	d.activeTasks.Store(3)
	d.ready.Store(true) // preflight done -> status should be "running"

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Desktop consumes these exact snake_case keys and the daemon host OS.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	if got, want := raw["cli_version"], "v9.9.9"; got != want {
		t.Errorf("cli_version key: got %v, want %q", got, want)
	}
	if got, want := raw["active_task_count"], float64(3); got != want {
		t.Errorf("active_task_count key: got %v, want %v", got, want)
	}
	if got, want := raw["status"], "running"; got != want {
		t.Errorf("status key: got %v, want %q", got, want)
	}
	if got, want := raw["os"], runtime.GOOS; got != want {
		t.Errorf("os key: got %v, want %q", got, want)
	}

	var resp healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode typed response: %v", err)
	}
	if resp.CLIVersion != "v9.9.9" {
		t.Errorf("CLIVersion: got %q, want %q", resp.CLIVersion, "v9.9.9")
	}
	if resp.ActiveTaskCount != 3 {
		t.Errorf("ActiveTaskCount: got %d, want 3", resp.ActiveTaskCount)
	}
}

// A bound health port remains "starting" until preflight marks the daemon ready.
func TestHealthHandlerReportsStartingUntilReady(t *testing.T) {
	t.Parallel()

	d := newHealthTestDaemon("v1.0.0")
	handler := d.healthHandler(time.Now())

	readStatus := func() string {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		var resp healthResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return resp.Status
	}

	if got := readStatus(); got != "starting" {
		t.Fatalf("status before ready: got %q, want \"starting\"", got)
	}

	d.ready.Store(true)

	if got := readStatus(); got != "running" {
		t.Fatalf("status after ready: got %q, want \"running\"", got)
	}
}

func TestHealthHandlerActiveTaskCountTracksCounter(t *testing.T) {
	t.Parallel()

	d := newHealthTestDaemon("v1.0.0")
	handler := d.healthHandler(time.Now())

	d.activeTasks.Add(1)
	d.activeTasks.Add(1)
	assertActiveTaskCount(t, handler, 2)

	d.activeTasks.Add(-1)
	assertActiveTaskCount(t, handler, 1)

	d.activeTasks.Add(-1)
	assertActiveTaskCount(t, handler, 0)
}

func TestShutdownHandlerPostCancelsDaemonContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &Daemon{cancelFunc: cancel}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	d.shutdownHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("daemon context was not cancelled after POST /shutdown")
	}
}

func TestShutdownHandlerRejectsNonPost(t *testing.T) {
	t.Parallel()

	cancelled := false
	d := &Daemon{cancelFunc: func() { cancelled = true }}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	d.shutdownHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	time.Sleep(10 * time.Millisecond)
	if cancelled {
		t.Fatal("GET request should not trigger cancellation")
	}
}

func TestHealthHandlerRespondsWhileTaskRepoLookupWaits(t *testing.T) {
	const workspaceID = "ws-health"
	const repoURL = "https://github.com/org/repo.git"
	cache := newBlockingLookupRepoCache("/cache/org/repo.git")
	d := &Daemon{
		cfg: Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{
			workspaceID: {
				workspaceID:     workspaceID,
				runtimeIDs:      []string{"rt-1"},
				allowedRepoURLs: map[string]struct{}{repoURL: {}},
				taskRepoURLs:    map[string]struct{}{},
			},
		},
		repoCache: cache,
		logger:    slog.Default(),
	}
	defer cache.release()

	registerDone := make(chan struct{})
	go func() {
		d.registerTaskRepos(workspaceID, []protocol.TaskRepository{{URL: repoURL}})
		close(registerDone)
	}()
	cache.waitForLookup(t)

	rec := httptest.NewRecorder()
	healthDone := make(chan struct{})
	go func() {
		d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		close(healthDone)
	}()

	select {
	case <-healthDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("/health blocked behind task repo cache lookup")
	}

	cache.release()
	select {
	case <-registerDone:
	case <-time.After(time.Second):
		t.Fatal("registerTaskRepos did not unblock after repo lookup finished")
	}
}

type blockingLookupRepoCache struct {
	path          string
	lookupSeen    chan struct{}
	releaseLookup chan struct{}
	releaseOnce   sync.Once
}

func newBlockingLookupRepoCache(path string) *blockingLookupRepoCache {
	return &blockingLookupRepoCache{
		path:          path,
		lookupSeen:    make(chan struct{}),
		releaseLookup: make(chan struct{}),
	}
}

func (c *blockingLookupRepoCache) Lookup(_, _ string) string {
	select {
	case <-c.lookupSeen:
	default:
		close(c.lookupSeen)
	}
	<-c.releaseLookup
	return c.path
}

func (c *blockingLookupRepoCache) Sync(string, []repocache.RepoInfo) error {
	return nil
}

func (c *blockingLookupRepoCache) WithRepoLock(_ string, fn func() error) error {
	return fn()
}

func (c *blockingLookupRepoCache) CreateWorktree(repocache.WorktreeParams) (*repocache.WorktreeResult, error) {
	return nil, nil
}

func (c *blockingLookupRepoCache) waitForLookup(t *testing.T) {
	t.Helper()
	select {
	case <-c.lookupSeen:
	case <-time.After(time.Second):
		t.Fatal("registerTaskRepos did not call repo lookup")
	}
}

func (c *blockingLookupRepoCache) release() {
	c.releaseOnce.Do(func() {
		close(c.releaseLookup)
	})
}

func assertActiveTaskCount(t *testing.T, h http.HandlerFunc, want int64) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var resp healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ActiveTaskCount != want {
		t.Errorf("active_task_count: got %d, want %d", resp.ActiveTaskCount, want)
	}
}
