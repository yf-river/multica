package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/repocache"
	"github.com/multica-ai/multica/server/pkg/agent"
)

var errTaskStartConflict = errors.New("task start skipped because task is no longer startable")

// ErrRepoNotConfigured is returned by ensureRepoReady when the requested repo
// URL is not present in the workspace's repo configuration after a fresh
// server refresh.
var ErrRepoNotConfigured = errors.New("repo is not configured for this workspace")

// ErrNoRuntimesToRegister is returned by registerRuntimesForWorkspace when
// the daemon has nothing to host on a workspace — typically a custom-only
// daemon whose only enabled custom runtime profile was just disabled, leaving
// zero built-in agents and zero resolvable profiles. Callers must
// differentiate by intent: initial registration (syncWorkspacesFromAPI's
// new-workspace branch) treats this as a config error and skips the
// workspace until something changes; the profile-drift refresh path
// (refreshWorkspaceRuntimeProfiles) treats it as a legitimate converged
// state and explicitly deregisters the now-stale local runtime IDs so the
// server marks them offline immediately instead of waiting on the 150 s
// stale-heartbeat sweep.
var ErrNoRuntimesToRegister = errors.New("no agent runtimes could be registered")

const (
	taskSlotWaitTimeout     = 2 * time.Second
	taskSlotCapacityBackoff = 5 * time.Second
)

func taskScopedAuthToken(task Task) (string, error) {
	token := strings.TrimSpace(task.AuthToken)
	if token == "" {
		return "", errors.New("server did not provide task-scoped auth token")
	}
	if !strings.HasPrefix(token, "mat_") {
		return "", errors.New("server provided non-task-scoped auth token")
	}
	return token, nil
}

// taskRunner executes a single agent task and returns the result.
// Extracted as an interface so tests can inject a fake without spawning real
// agent processes, while keeping test scaffolding out of the production struct.
type taskRunner interface {
	run(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error)
}

// taskRunnerFunc adapts a plain function to the taskRunner interface.
type taskRunnerFunc func(context.Context, Task, string, int, *slog.Logger) (TaskResult, error)

func (f taskRunnerFunc) run(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error) {
	return f(ctx, task, provider, slot, log)
}

var (
	// detectAgentVersion / checkAgentMinVersion are indirections over the
	// real agent helpers so tests can run the registration path without
	// shelling out to a real CLI. Mirrors the pattern used for the brew
	// helpers above.
	detectAgentVersion   = agent.DetectVersion
	checkAgentMinVersion = agent.CheckMinVersion

	// lookPath is an indirection over exec.LookPath so registration tests can
	// resolve custom runtime-profile commands without manipulating the
	// process PATH. Mirrors the detectAgentVersion hook above.
	lookPath = exec.LookPath

	// profilePathExecutable reports whether path points at an existing,
	// non-directory file with at least one executable bit set. It is the
	// gate appendProfileRuntimes uses before trusting a per-machine command
	// path override (MUL-3284) — a stale or mistyped override must fall back
	// to the PATH lookup rather than register a runtime that can't launch.
	// Indirected as a package var so tests can assert override preference
	// without staging a real executable on disk.
	profilePathExecutable = func(path string) bool {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return false
		}
		return info.Mode().Perm()&0o111 != 0
	}
)

// workspaceState tracks registered runtimes for a single workspace.
//
// allowedRepoURLs covers the workspace-level repo bindings; it gets rebuilt on
// every refresh from the server. taskRepoURLs covers repos that the server
// surfaced through a per-task claim (project github_repo resources today,
// possibly other typed sources later) — those don't show up in
// GetWorkspaceRepos, so they would be wiped on refresh if we shared one map.
type workspaceState struct {
	workspaceID     string
	runtimeIDs      []string
	reposVersion    string // stored for future use: skip refresh when version unchanged
	allowedRepoURLs map[string]struct{}
	taskRepoURLs    map[string]struct{}
	settings        json.RawMessage // workspace settings (JSONB)
	lastRepoSyncErr string
	repoRefreshMu   sync.Mutex
	// profileSetSig is a content hash of the workspace's custom runtime
	// profile list (MUL-3332) as last seen from the server. The
	// workspaceSyncLoop compares the live signature with this cached value;
	// any drift triggers a re-register so newly-added (or edited / disabled)
	// custom runtimes appear without a daemon restart. Empty before the
	// first successful profile fetch (older server / network blip); guarded
	// by Daemon.mu like every other field on this struct.
	profileSetSig string
}

type repoCacheBackend interface {
	Lookup(workspaceID, url string) string
	Sync(workspaceID string, repos []repocache.RepoInfo) error
	WithRepoLock(barePath string, fn func() error) error
	CreateWorktree(params repocache.WorktreeParams) (*repocache.WorktreeResult, error)
}

// Daemon is the local agent runtime that polls for and executes tasks.
type Daemon struct {
	cfg       Config
	client    *Client
	repoCache repoCacheBackend
	logger    *slog.Logger

	mu           sync.Mutex
	workspaces   map[string]*workspaceState
	runtimeIndex map[string]Runtime // runtimeID -> Runtime for provider lookups
	// profileCommandPaths maps a custom runtime profile_id -> the absolute
	// executable path resolved on PATH for that profile's command_name
	// (MUL-3284). Populated in registerRuntimesForWorkspace when a profile's
	// command resolves; read by runTask via customCommandPathForRuntime to
	// launch the custom command for a claimed task. Guarded by mu.
	profileCommandPaths map[string]string
	reloading           sync.Mutex         // prevents concurrent workspace syncs
	runtimeSet          *runtimeSetWatcher // multi-subscriber pub/sub for runtime-set changes

	versionsMu    sync.RWMutex      // guards agentVersions
	agentVersions map[string]string // provider -> detected CLI version (set during registration)

	wsHBMu      sync.RWMutex         // guards wsHBLastAck
	wsHBLastAck map[string]time.Time // runtime_id -> last successful WS heartbeat ack timestamp

	// runtimeGoneMu guards runtimeGoneInflight, reregisterNextAttempt, and
	// reregisterLastCompletedAt. The state lets heartbeat / poller / WS-ack
	// handlers converge on a single recovery path when they each detect that a
	// runtime row was deleted server-side without three of them stampeding
	// registerRuntimesForWorkspace.
	runtimeGoneMu             sync.Mutex
	runtimeGoneInflight       map[string]struct{}  // runtime_id -> currently recovering
	reregisterNextAttempt     map[string]time.Time // workspace_id -> earliest time the next re-register attempt may run
	reregisterLastCompletedAt map[string]time.Time // workspace_id -> wall-clock at which the last SUCCESSFUL re-register call returned (failures intentionally not stamped — see recordRegisterCompletion)

	cancelFunc  context.CancelFunc // set by Run(); used by Shutdown and health handling
	rootCtx     context.Context    // set by Run(); used by long-running recoveries that must survive per-runtime ctx cancellation
	activeTasks atomic.Int64       // number of tasks currently in handleTask; exposed via /health
	ready       atomic.Bool        // false until preflight completes; gates /health status (starting -> running)

	activeEnvRootsMu sync.Mutex
	activeEnvRoots   map[string]int // env root path -> reference count (handles reuse paths marked twice)

	// localPathLocks is kept for legacy local_directory compatibility paths.
	// Normal issue tasks use issue-scoped managed worktrees, so project
	// local_directory resources no longer override the task workdir.
	localPathLocks *LocalPathLocker

	runtimeLastTaskStart map[string]time.Time

	// bgSyncs tracks background goroutines started by registerTaskRepos so
	// callers (notably tests using t.TempDir-backed cache roots) can wait for
	// them to drain before tearing the daemon down. Without this the bg
	// goroutine can race against t.TempDir cleanup, leaving a partially
	// deleted bare clone and an unrelated `not empty` cleanup failure.
	bgSyncs sync.WaitGroup

	runner             taskRunner    // executes agent tasks; set to d.runTask by New(), overridable in tests
	cancelPollInterval time.Duration // how often handleTask polls for server-side cancellation; overridable in tests
	codexBrokers       map[string]*agent.CodexBrokerBackend
}

// New creates a new Daemon instance.
func New(cfg Config, logger *slog.Logger) *Daemon {
	cacheRoot := filepath.Join(cfg.WorkspacesRoot, ".repos")
	client := NewClient(cfg.ServerBaseURL)
	// Tag every daemon HTTP request with the daemon's CLI version so the
	// server can split logs/metrics by client version (parallel to the CLI).
	client.SetVersion(cfg.CLIVersion)
	d := &Daemon{
		cfg:                       cfg,
		client:                    client,
		repoCache:                 repocache.New(cacheRoot, logger),
		logger:                    logger,
		workspaces:                make(map[string]*workspaceState),
		runtimeIndex:              make(map[string]Runtime),
		profileCommandPaths:       make(map[string]string),
		runtimeSet:                newRuntimeSetWatcher(),
		agentVersions:             make(map[string]string),
		wsHBLastAck:               make(map[string]time.Time),
		activeEnvRoots:            make(map[string]int),
		localPathLocks:            NewLocalPathLocker(),
		runtimeLastTaskStart:      make(map[string]time.Time),
		runtimeGoneInflight:       make(map[string]struct{}),
		reregisterNextAttempt:     make(map[string]time.Time),
		reregisterLastCompletedAt: make(map[string]time.Time),
		cancelPollInterval:        5 * time.Second,
		codexBrokers:              make(map[string]*agent.CodexBrokerBackend),
	}
	d.runner = taskRunnerFunc(d.runTask)
	return d
}

func (d *Daemon) codexBrokerBackend(runtimeID string, cfg agent.Config) agent.Backend {
	key := strings.TrimSpace(runtimeID)
	if key == "" {
		key = "default"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if backend := d.codexBrokers[key]; backend != nil {
		backend.UpdateConfig(cfg)
		return backend
	}
	backend := agent.NewCodexBrokerBackend(cfg)
	d.codexBrokers[key] = backend
	return backend
}

// setAgentVersion records the detected CLI version for an agent provider so
// later task-dispatch code (e.g. Codex sandbox policy) can read it.
func (d *Daemon) setAgentVersion(provider, version string) {
	d.versionsMu.Lock()
	defer d.versionsMu.Unlock()
	d.agentVersions[provider] = version
}

// agentVersion returns the last-detected CLI version for an agent provider,
// or an empty string if unknown.
func (d *Daemon) agentVersion(provider string) string {
	d.versionsMu.RLock()
	defer d.versionsMu.RUnlock()
	return d.agentVersions[provider]
}

func (d *Daemon) notifyRuntimeSetChanged() {
	d.runtimeSet.notify()
}

// reregisterCoalesceWindow caps how often the daemon re-registers a workspace
// after detecting a runtime_not_found response. Many stale runtime IDs may be
// reported within seconds of each other (one delete clears all of a daemon's
// runtimes), and a single re-register call replaces every runtime in the
// workspace, so concurrent recoveries must collapse to one API call.
const reregisterCoalesceWindow = 30 * time.Second

// reregisterFailureBackoff is the additional wait inserted before the next
// re-register attempt when the previous one failed. This prevents heartbeat
// ticks (~15s) from converting a server-side log flood into a re-register
// flood when re-registration itself is failing (workspace removed, server
// unreachable, ...).
const reregisterFailureBackoff = 60 * time.Second

// handleRuntimeGone is the single recovery entry point shared by the HTTP
// heartbeat path, the runtime poller, and the WebSocket runtime_gone ack
// handler. All three may notice the same stale runtime within a few ms of
// each other, so this function:
//
//   - keys an in-flight set on runtimeID to drop concurrent calls for the same
//     ID after the first one is already cleaning up;
//   - keys a per-workspace next-attempt timestamp on workspaceID so that
//     concurrent recoveries triggered by the SAME initial event coalesce to a
//     single registerRuntimesForWorkspace call. The slot is cleared on success
//     so a later distinct runtime deletion in the same workspace can trigger
//     its own recovery without waiting for the coalesce window to expire; and
//   - keys a per-workspace last-completed timestamp so that a straggler whose
//     removeStaleRuntime took long enough that a sibling fully ran AND cleared
//     the slot can still recognize itself as same-wave and bail. Without this,
//     the success-case slot clear opens a race where the late caller re-claims
//     an empty slot and double-registers.
//
// On failure of the underlying re-register, the next-attempt timestamp is
// extended by reregisterFailureBackoff so we don't replace a server-side log
// flood with a daemon-side register flood. workspaceSyncLoop will retry
// independently every DefaultWorkspaceSyncInterval as a safety net.
//
// The recovery HTTP call uses the daemon root context, not the caller's. The
// heartbeat path's per-runtime ctx is cancelled by notifyRuntimeSetChanged the
// moment we prune the dead UUID, and if we forwarded that ctx the in-flight
// register would self-cancel mid-flight.
func (d *Daemon) handleRuntimeGone(runtimeID string) {
	if runtimeID == "" {
		return
	}

	// entryAt anchors the same-wave-straggler check at the bottom of the
	// function. Captured at the very top so removeStaleRuntime mutex
	// contention can't push it past a sibling's register completion.
	entryAt := time.Now()

	// Stampede control per runtime ID.
	d.runtimeGoneMu.Lock()
	if _, inflight := d.runtimeGoneInflight[runtimeID]; inflight {
		d.runtimeGoneMu.Unlock()
		return
	}
	d.runtimeGoneInflight[runtimeID] = struct{}{}
	d.runtimeGoneMu.Unlock()
	defer func() {
		d.runtimeGoneMu.Lock()
		delete(d.runtimeGoneInflight, runtimeID)
		d.runtimeGoneMu.Unlock()
	}()

	workspaceID, removed := d.removeStaleRuntime(runtimeID)
	if !removed {
		// Already gone from local state — a parallel recovery already
		// cleaned this up, or workspaceSyncLoop pruned the whole workspace.
		return
	}

	d.logger.Info("runtime deleted server-side; pruned from local state",
		"runtime_id", runtimeID, "workspace_id", workspaceID)
	d.notifyRuntimeSetChanged()

	if !d.tryClaimRegisterSlot(workspaceID, entryAt, time.Now()) {
		d.logger.Debug("skip re-register: coalescing with recent attempt",
			"workspace_id", workspaceID)
		return
	}

	err := d.reregisterWorkspaceAfterRuntimeGone(d.recoveryContext(), workspaceID)
	d.recordRegisterCompletion(workspaceID, time.Now(), err)
	if err != nil {
		// Logged at Warn (not Error) because workspaceSyncLoop retries
		// independently every DefaultWorkspaceSyncInterval, so a transient
		// failure here is not a stuck state — just an extra wait.
		d.logger.Warn("re-register after runtime gone failed",
			"workspace_id", workspaceID, "error", err)
	}
}

// tryClaimRegisterSlot atomically decides whether the calling goroutine should
// run registerRuntimesForWorkspace. Returns true and claims the in-flight slot
// when the caller may proceed; returns false (without mutating state) when the
// call must be coalesced with a peer.
//
// Two gates are checked under runtimeGoneMu:
//
//  1. reregisterNextAttempt: a future timestamp means a peer holds the slot or
//     a previous attempt failed and we are inside the failure backoff window.
//  2. reregisterLastCompletedAt: a timestamp at or after our entryAt means a
//     peer's register SUCCEEDED after we entered handleRuntimeGone, so the
//     workspace state is already covered for our wave and we can bail.
//     Failures intentionally don't stamp this field (see
//     recordRegisterCompletion), so a same-wave straggler whose entryAt
//     predates a failed sibling can still retry once the failure backoff
//     expires — failures don't cover anything.
//
// entryAt is the wall-clock captured at the top of handleRuntimeGone. now is
// passed in (rather than read inside) so tests can drive the gate
// deterministically without sleeping.
