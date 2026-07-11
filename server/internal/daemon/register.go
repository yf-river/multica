package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func (d *Daemon) tryClaimRegisterSlot(workspaceID string, entryAt, now time.Time) bool {
	d.runtimeGoneMu.Lock()
	defer d.runtimeGoneMu.Unlock()
	if next, ok := d.reregisterNextAttempt[workspaceID]; ok && now.Before(next) {
		return false
	}
	if last, ok := d.reregisterLastCompletedAt[workspaceID]; ok && !last.Before(entryAt) {
		return false
	}
	d.reregisterNextAttempt[workspaceID] = now.Add(reregisterCoalesceWindow)
	return true
}

// recordRegisterCompletion records the outcome of a register call. On success
// it stamps lastCompletedAt (which suppresses same-wave stragglers via
// tryClaimRegisterSlot) and clears the in-flight slot so a genuinely later
// runtime deletion can claim immediately. On failure it extends
// reregisterNextAttempt by the failure backoff and intentionally does NOT
// stamp lastCompletedAt — a failed register did not cover any workspace
// state, so a same-wave straggler whose entryAt predates the failure must
// still be allowed to retry once the backoff expires. workspaceSyncLoop only
// retries when the workspace's runtimeIDs fully drain, so partial-deletion
// recovery has to come from the straggler path.
func (d *Daemon) recordRegisterCompletion(workspaceID string, completedAt time.Time, err error) {
	d.runtimeGoneMu.Lock()
	defer d.runtimeGoneMu.Unlock()
	if err != nil {
		d.reregisterNextAttempt[workspaceID] = completedAt.Add(reregisterFailureBackoff)
		return
	}
	d.reregisterLastCompletedAt[workspaceID] = completedAt
	delete(d.reregisterNextAttempt, workspaceID)
}

// recoveryContext returns the daemon root context for long-running recovery
// HTTP calls (re-register, recover-orphans) that must survive the heartbeat
// loop tearing down a per-runtime context. Falls back to Background when the
// daemon was not started via Run(), e.g. unit-test fixtures.
func (d *Daemon) recoveryContext() context.Context {
	if d.rootCtx != nil {
		return d.rootCtx
	}
	return context.Background()
}

// removeStaleRuntime drops a runtime ID from its owning workspace's runtimeIDs
// list, the daemon-level runtimeIndex, and the WS heartbeat freshness map.
// Returns the workspace ID and true if the runtime was tracked, "" and false
// otherwise.
//
// Callers must NOT replace workspaceState pointers — only mutate fields in
// place — because ensureRepoReady holds workspaceState.repoRefreshMu through
// long repo-sync calls. See syncWorkspacesFromAPI for the same invariant.
func (d *Daemon) removeStaleRuntime(runtimeID string) (string, bool) {
	d.mu.Lock()
	var workspaceID string
	for wsID, ws := range d.workspaces {
		found := false
		filtered := ws.runtimeIDs[:0:0]
		for _, rid := range ws.runtimeIDs {
			if rid == runtimeID {
				found = true
				continue
			}
			filtered = append(filtered, rid)
		}
		if found {
			ws.runtimeIDs = filtered
			workspaceID = wsID
			break
		}
	}
	if workspaceID == "" {
		d.mu.Unlock()
		return "", false
	}
	delete(d.runtimeIndex, runtimeID)
	d.mu.Unlock()

	d.wsHBMu.Lock()
	delete(d.wsHBLastAck, runtimeID)
	d.wsHBMu.Unlock()

	return workspaceID, true
}

// workspaceNeedsRuntimeRecovery reports whether a tracked workspace currently
// has zero runtime IDs — the state reached when handleRuntimeGone pruned every
// runtime and its inline re-register failed. workspaceSyncLoop calls this on
// each tick so the workspace can recover without waiting for an external
// trigger.
func (d *Daemon) workspaceNeedsRuntimeRecovery(workspaceID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		return false
	}
	return len(ws.runtimeIDs) == 0
}

// reregisterWorkspaceAfterRuntimeGone calls registerRuntimesForWorkspace and
// updates the existing workspaceState in place. The register response is
// authoritative for this workspace's runtime set — every configured provider
// is included, with UpsertAgentRuntime returning the same row ID for surviving
// providers and a fresh ID for any that were deleted server-side. Replacing
// (rather than appending) is required: a partial recovery, where only one
// runtime in a multi-provider workspace was deleted, would otherwise produce
// duplicates for every provider that wasn't deleted.
//
// The workspaceState pointer is NEVER replaced (see syncWorkspacesFromAPI's
// invariant about repoRefreshMu). Only fields are mutated.
// applyRegisterResponseInPlace folds a fresh /api/daemon/register response
// back into the workspaceState and runtimeIndex without replacing the
// workspaceState pointer (see syncWorkspacesFromAPI's invariant about
// repoRefreshMu). It is the shared converger used by both the runtime_gone
// recovery and the profile-drift refresh; the two callers differ only in
// follow-up side effects (RecoverOrphans / Deregister), so those stay at the
// call site.
//
// Returns:
//   - newIDs:     the runtime IDs the server returned in this response, in
//     the order they were returned. These are the daemon's authoritative
//     current runtime set after the call.
//   - droppedIDs: runtime IDs that were tracked before this call but did
//     NOT survive the response. Drift callers Deregister these so the
//     server marks them offline immediately instead of waiting on the 150 s
//     stale-heartbeat sweep; the runtime_gone path can ignore them because
//     those rows were already deleted server-side.
//   - ok:         false when the workspace was forgotten between the
//     register call and this apply (e.g. the user left the workspace and
//     syncWorkspacesFromAPI removed it). The caller must abort silently in
//     that case — there is no state left to update.
//
// profileSig is the digest captured during the register; an empty value is
// the explicit "fetch failed, keep the previous signature" sentinel from
// appendProfileRuntimes.
func (d *Daemon) applyRegisterResponseInPlace(workspaceID string, resp *RegisterResponse, profileSig string) (newIDs, droppedIDs []string, ok bool) {
	newIDs = make([]string, 0, len(resp.Runtimes))
	newIDSet := make(map[string]struct{}, len(resp.Runtimes))
	for _, rt := range resp.Runtimes {
		newIDs = append(newIDs, rt.ID)
		newIDSet[rt.ID] = struct{}{}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	ws, exists := d.workspaces[workspaceID]
	if !exists {
		return nil, nil, false
	}
	// Drop runtimeIndex entries for prior runtime IDs that the server did not
	// return — typically there are none for upsert-on-existing-provider, but
	// a daemon config change (provider removed) or a profile disable would
	// leak entries otherwise.
	for _, oldID := range ws.runtimeIDs {
		if _, kept := newIDSet[oldID]; !kept {
			delete(d.runtimeIndex, oldID)
			droppedIDs = append(droppedIDs, oldID)
		}
	}
	for _, rt := range resp.Runtimes {
		d.runtimeIndex[rt.ID] = rt
	}
	// Response is authoritative — replace, do not append. Replacing also
	// catches the rare case where UpsertAgentRuntime returns a different ID
	// for a surviving provider (e.g. schema change); the daemon converges on
	// what the server says without leaving stale heartbeat goroutines.
	ws.runtimeIDs = newIDs
	if resp.ReposVersion != "" {
		ws.reposVersion = resp.ReposVersion
		ws.allowedRepoURLs = repoAllowlist(resp.Repos)
	}
	if len(resp.Settings) > 0 {
		ws.settings = resp.Settings
	}
	// Refresh the cached profile signature only when the fetch succeeded;
	// an empty sig means the GetRuntimeProfiles call failed and we must
	// preserve the previous signature so the next sync tick can still
	// detect a real drift instead of falsely thinking everything is in sync.
	if profileSig != "" {
		ws.profileSetSig = profileSig
	}
	return newIDs, droppedIDs, true
}

func (d *Daemon) reregisterWorkspaceAfterRuntimeGone(ctx context.Context, workspaceID string) error {
	resp, profileSig, err := d.registerRuntimesForWorkspace(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("register runtimes: %w", err)
	}

	newIDs, _, ok := d.applyRegisterResponseInPlace(workspaceID, resp, profileSig)
	if !ok {
		return fmt.Errorf("workspace %s no longer tracked", workspaceID)
	}

	for _, rid := range newIDs {
		d.logger.Info("re-registered runtime after server-side deletion",
			"workspace_id", workspaceID, "runtime_id", rid)
	}
	d.notifyRuntimeSetChanged()

	// Tell the server about any tasks the previous (now-deleted) runtime
	// was working on, mirroring the registration path's recover-orphans call.
	// This is intentionally scoped to the runtime_gone recovery: the
	// runtimes were truly gone server-side, so anything still in
	// dispatched/running/waiting_local_directory on those rows is an orphan
	// that needs to be failed-and-retried. The drift-refresh path (which
	// also feeds applyRegisterResponseInPlace) deliberately skips this step
	// because its surviving runtime IDs may still be actively executing
	// tasks for the user (MUL-3332).
	for _, rid := range newIDs {
		if err := d.client.RecoverOrphans(ctx, rid); err != nil {
			d.logger.Warn("recover-orphans after re-register failed",
				"runtime_id", rid, "error", err)
		}
	}
	return nil
}

// runtimeSetWatcher is a tiny pub/sub for runtime-set changes. It exists
// because more than one supervisor (taskWakeupLoop, heartbeatLoop, pollLoop)
// needs to react to runtime-set changes; a single buffered channel would
// race so only the first listener would learn about each change.
//
// Each subscriber gets a 1-slot channel; missed nudges coalesce into a
// single signal — the subscriber is expected to re-derive the current
// runtime set via allRuntimeIDs() rather than relying on edge counts.
type runtimeSetWatcher struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]struct{}
}

func newRuntimeSetWatcher() *runtimeSetWatcher {
	return &runtimeSetWatcher{subscribers: make(map[chan struct{}]struct{})}
}

// Subscribe returns a channel that receives a non-blocking nudge whenever
// the runtime set changes, and an unsubscribe func the caller must invoke
// when done.
func (w *runtimeSetWatcher) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	w.subscribers[ch] = struct{}{}
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		delete(w.subscribers, ch)
		w.mu.Unlock()
	}
}

func (w *runtimeSetWatcher) notify() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// wsHeartbeatFreshness defines how long a WS heartbeat ack is considered
// "fresh enough" to suppress the HTTP heartbeat for that runtime. The window
// is 2× HeartbeatInterval so a single dropped WS ack still keeps HTTP
// suppressed, but two missed acks (~30s of WS silence) re-enable HTTP — well
// inside the server-side 45s offline threshold.
func (d *Daemon) wsHeartbeatFreshness() time.Duration {
	if d.cfg.HeartbeatInterval <= 0 {
		return 30 * time.Second
	}
	return 2 * d.cfg.HeartbeatInterval
}

// recordWSHeartbeatAck stamps the runtime as having received a fresh WS
// heartbeat ack from the server. Called by the WS read pump.
func (d *Daemon) recordWSHeartbeatAck(runtimeID string) {
	if runtimeID == "" {
		return
	}
	d.wsHBMu.Lock()
	d.wsHBLastAck[runtimeID] = time.Now()
	d.wsHBMu.Unlock()
}

// wsHeartbeatRecentlyAcked reports whether the runtime received a WS
// heartbeat ack inside the freshness window. The HTTP heartbeat loop uses
// this to skip duplicate work when WS is already keeping the runtime alive.
func (d *Daemon) wsHeartbeatRecentlyAcked(runtimeID string) bool {
	d.wsHBMu.RLock()
	last, ok := d.wsHBLastAck[runtimeID]
	d.wsHBMu.RUnlock()
	if !ok {
		return false
	}
	return time.Since(last) < d.wsHeartbeatFreshness()
}

// clearWSHeartbeatAcks drops all WS heartbeat freshness records. Called on
// WS disconnect so HTTP heartbeats resume on the next tick.
func (d *Daemon) clearWSHeartbeatAcks() {
	d.wsHBMu.Lock()
	for k := range d.wsHBLastAck {
		delete(d.wsHBLastAck, k)
	}
	d.wsHBMu.Unlock()
}

// Run starts the daemon: resolves auth, registers runtimes, then polls for tasks.
func (d *Daemon) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	d.cancelFunc = cancel
	d.rootCtx = ctx

	// Bind health port early to detect another running daemon.
	healthLn, err := d.listenHealth()
	if err != nil {
		return err
	}

	agentNames := make([]string, 0, len(d.cfg.Agents))
	for name := range d.cfg.Agents {
		agentNames = append(agentNames, name)
	}
	logFields := []any{"version", d.cfg.CLIVersion, "agents", agentNames, "server", d.cfg.ServerBaseURL}
	if d.cfg.Profile != "" {
		logFields = append(logFields, "profile", d.cfg.Profile)
	}
	d.logger.Info("starting daemon", logFields...)
	d.logger.Debug("daemon config resolved",
		"daemon_id", d.cfg.DaemonID,
		"device_name", d.cfg.DeviceName,
		"workspaces_root", d.cfg.WorkspacesRoot,
		"health_port", d.cfg.HealthPort,
		"poll_interval", d.cfg.PollInterval,
		"heartbeat_interval", d.cfg.HeartbeatInterval,
		"agent_timeout", d.cfg.AgentTimeout,
		"idle_watchdog", d.cfg.AgentIdleWatchdog,
		"max_concurrent_tasks", d.cfg.MaxConcurrentTasks,
		"gc_enabled", d.cfg.GCEnabled,
		"launched_by", d.cfg.LaunchedBy,
	)

	// Load auth token from CLI config.
	if err := d.resolveAuth(); err != nil {
		return err
	}

	// Bind and serve the health port before the (potentially slow) preflight,
	// so `daemon start` and the desktop see a live "starting" daemon instead
	// of connection-refused while preflightAuth runs. preflightAuth's initial
	// workspace sync detects every configured agent's version by exec'ing it,
	// which on a cold cache with many agents takes ~20s. Liveness (port up) and
	// readiness (status:"running") are reported separately: /health stays
	// "starting" until d.ready is set after preflight, so a slow or *failing*
	// preflight is never misreported as a started daemon. resolveAuth has
	// already run, so a missing token still fails fast before we begin serving.
	go d.serveHealth(ctx, healthLn, time.Now())

	// Renew the PAT before the first API call, then do the initial
	// workspace sync. Both steps live in preflightAuth so the ordering
	// invariant (renew first) is enforced at one site instead of
	// scattered into Run, and tests can exercise the failure paths
	// without the full Run setup.
	if err := d.preflightAuth(ctx); err != nil {
		return err
	}

	// Deregister runtimes on shutdown (uses a fresh context since ctx will be cancelled).
	defer d.deregisterRuntimes()

	// Start workspace sync loop to discover newly created workspaces.
	go d.workspaceSyncLoop(ctx)

	taskWakeups := make(chan taskWakeup, 256)
	go d.taskWakeupLoop(ctx, taskWakeups)
	go d.heartbeatLoop(ctx)
	go d.gcLoop(ctx)
	go d.tokenRenewalLoop(ctx)

	// Preflight succeeded and the background loops are up: the daemon has
	// registered its runtimes and can now claim and run tasks. Flip /health
	// from "starting" to "running" — this is the signal `daemon start`'s
	// readiness wait blocks on, so success is reported only after startup
	// actually completed, not merely because the health port came up.
	d.ready.Store(true)
	d.logger.Debug("background loops launched (workspace-sync, task-wakeup, heartbeat, gc, token-renewal); health now reporting ready")
	err = d.pollLoop(ctx, taskWakeups)
	d.logger.Debug("daemon main loop returning", "error", err)
	return err
}

// deregisterRuntimes notifies the server that all runtimes are going offline.
func (d *Daemon) deregisterRuntimes() {
	runtimeIDs := d.allRuntimeIDs()
	if len(runtimeIDs) == 0 {
		d.logger.Debug("deregister: no runtimes to deregister")
		return
	}

	d.logger.Debug("deregistering runtimes on shutdown", "count", len(runtimeIDs), "runtime_ids", runtimeIDs)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := d.client.Deregister(ctx, runtimeIDs); err != nil {
		d.logger.Warn("failed to deregister runtimes on shutdown", "error", err)
	} else {
		d.logger.Info("deregistered runtimes", "count", len(runtimeIDs))
	}
}

// resolveAuth loads the auth token from the CLI config for the active profile.
func (d *Daemon) resolveAuth() error {
	cfg, err := cli.LoadCLIConfigForProfile(d.cfg.Profile)
	if err != nil {
		return fmt.Errorf("load CLI config: %w", err)
	}
	if cfg.Token == "" {
		loginHint := "'multica login'"
		if d.cfg.Profile != "" {
			loginHint = fmt.Sprintf("'multica login --profile %s'", d.cfg.Profile)
		}
		d.logger.Warn("not authenticated — run " + loginHint + " to authenticate, then restart the daemon")
		return fmt.Errorf("not authenticated: run %s first", loginHint)
	}
	d.client.SetToken(cfg.Token)
	d.logger.Info("authenticated")
	d.logger.Debug("auth token loaded", "profile", d.cfg.Profile, "token_len", len(cfg.Token))
	return nil
}

// allRuntimeIDs returns all runtime IDs across all watched workspaces.
func (d *Daemon) allRuntimeIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ids []string
	for _, ws := range d.workspaces {
		ids = append(ids, ws.runtimeIDs...)
	}
	return ids
}

// findRuntime looks up a Runtime by its ID.
func (d *Daemon) findRuntime(id string) *Runtime {
	d.mu.Lock()
	defer d.mu.Unlock()
	if rt, ok := d.runtimeIndex[id]; ok {
		return &rt
	}
	return nil
}

// recordProfileCommandPath remembers the absolute executable path resolved
// for a custom runtime profile's command_name. Called from
// registerRuntimesForWorkspace. Lazily initializes the map so test fixtures
// that build a Daemon literal without seeding every map don't panic.
func (d *Daemon) recordProfileCommandPath(profileID, path string) {
	if profileID == "" || path == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.profileCommandPaths == nil {
		d.profileCommandPaths = make(map[string]string)
	}
	d.profileCommandPaths[profileID] = path
}

// customCommandPathForRuntime returns the resolved custom executable path for
// a claimed task's RuntimeID, and whether the runtime is a custom-profile
// runtime. It returns ("", false) for built-in runtimes (no profile) and for
// runtimes whose profile command was never resolved on this host. runTask
// uses this to override the launch path so a custom runtime can run even when
// the host has no built-in agent of the same provider installed.
func (d *Daemon) customCommandPathForRuntime(runtimeID string) (string, bool) {
	if runtimeID == "" {
		return "", false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rt, ok := d.runtimeIndex[runtimeID]
	if !ok || rt.ProfileID == "" {
		return "", false
	}
	path, ok := d.profileCommandPaths[rt.ProfileID]
	if !ok || path == "" {
		return "", false
	}
	return path, true
}

func (d *Daemon) registerRuntimesForWorkspace(ctx context.Context, workspaceID string) (*RegisterResponse, string, error) {
	d.logger.Debug("registering runtimes for workspace", "workspace_id", workspaceID, "agent_count", len(d.cfg.Agents))
	var runtimes []map[string]any
	for name, entry := range d.cfg.Agents {
		version, err := detectAgentVersion(ctx, entry.Path)
		if err != nil {
			d.logger.Warn("skip registering runtime", "name", name, "error", err)
			continue
		}
		if err := checkAgentMinVersion(name, version); err != nil {
			d.logger.Warn("skip registering runtime: version too old", "name", name, "version", version, "error", err)
			continue
		}
		d.setAgentVersion(name, version)
		d.logger.Debug("agent version detected", "name", name, "version", version, "path", entry.Path)
		displayName := strings.ToUpper(name[:1]) + name[1:]
		if d.cfg.DeviceName != "" {
			displayName = fmt.Sprintf("%s (%s)", displayName, d.cfg.DeviceName)
		}
		runtimes = append(runtimes, map[string]any{
			"name":    displayName,
			"type":    name,
			"version": version,
			"status":  "online",
		})
	}

	// Append any workspace custom runtime profiles whose command resolves on
	// this host (MUL-3284). This is best-effort: a fetch error (e.g. an older
	// server returning 404) must never fail registration — the daemon simply
	// continues with the built-in runtimes it already collected. A profile
	// whose command_name is not on PATH is skipped (the host doesn't have it).
	//
	// profileSig is a content hash of the workspace's profile list captured
	// here so the workspaceSyncLoop can detect server-side profile changes
	// between sync ticks without making an extra round trip on every tick
	// (MUL-3332). An empty string means the fetch failed and the caller must
	// keep whatever signature was previously cached on the workspaceState.
	profileSig := d.appendProfileRuntimes(ctx, workspaceID, &runtimes)

	if len(runtimes) == 0 {
		// profileSig is still meaningful even when nothing resolves: the
		// drift-refresh path uses it to remember "we already converged on the
		// disabled-everywhere state" so the next sync tick is a no-op instead
		// of a re-empty-register loop. Initial-registration callers that don't
		// care about the sig discard it via _.
		return nil, profileSig, ErrNoRuntimesToRegister
	}

	req := map[string]any{
		"workspace_id": workspaceID,
		"daemon_id":    d.cfg.DaemonID,
		"device_name":  d.cfg.DeviceName,
		"cli_version":  d.cfg.CLIVersion,
		"launched_by":  d.cfg.LaunchedBy,
		"runtimes":     runtimes,
	}

	resp, err := d.client.Register(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("register runtimes: %w", err)
	}
	if len(resp.Runtimes) == 0 {
		return nil, "", fmt.Errorf("register runtimes: empty response")
	}
	d.logger.Debug("register response", "workspace_id", workspaceID, "runtimes", len(resp.Runtimes), "repos", len(resp.Repos), "repos_version", resp.ReposVersion)
	return resp, profileSig, nil
}

// appendProfileRuntimes fetches the workspace's enabled custom runtime
// profiles (MUL-3284) and appends a runtime registration entry for each one
// whose command_name resolves on this host's PATH. For each resolved profile
// it records the absolute command path keyed by profile_id (via
// recordProfileCommandPath) so runTask can later launch the custom executable
// for a claimed task.
//
// Best-effort by contract: any error fetching profiles (older server, network
// blip) is logged and swallowed — registration proceeds with the built-in
// runtimes already collected. A profile whose command is not on PATH is
// skipped with an Info log (this host simply doesn't have that command).
//
// The registration entry mirrors the built-in shape: name = display_name
// (suffixed with the device name like the built-in path), type =
// protocol_family (the routing provider), version = best-effort detected
// version, status = "online", plus the profile_id the server validates.
//
// Returns a content signature of the fetched profile list (MUL-3332). The
// signature is used by the workspace sync loop to detect server-side profile
// changes between sync ticks and trigger a re-register without a daemon
// restart. Returns the empty string when the fetch failed — callers must
// treat that as "unknown, do not overwrite a previously-stored signature"
// (otherwise a transient 5xx would silently flip the daemon into thinking the
// workspace has zero profiles).
func (d *Daemon) appendProfileRuntimes(ctx context.Context, workspaceID string, runtimes *[]map[string]any) string {
	resp, err := d.client.GetRuntimeProfiles(ctx, workspaceID)
	if err != nil {
		// Best-effort: never fail registration because profiles couldn't be
		// fetched. An older server with no profiles route returns 404.
		d.logger.Info("skip custom runtime profiles: fetch failed (continuing with built-in runtimes)",
			"workspace_id", workspaceID, "error", err)
		return ""
	}
	if resp == nil {
		// Empty payload — same shape as "server has zero profiles". Return
		// the digest of an empty list so the sync loop can still detect a
		// later transition (zero → first profile added).
		return profileSetSignature(nil)
	}
	for _, profile := range resp.RuntimeProfiles {
		if profile.CommandName == "" || profile.ProtocolFamily == "" {
			d.logger.Warn("skip custom runtime profile: missing command_name or protocol_family",
				"workspace_id", workspaceID, "profile_id", profile.ID, "display_name", profile.DisplayName)
			continue
		}
		// Resolve the executable to launch for this profile. A per-machine
		// path override (MUL-3284, `multica runtime profile set-path`) wins
		// over the PATH lookup when it is set AND points at a real
		// executable — this is how an operator pins a profile to a binary
		// that isn't on the daemon's PATH, or selects between multiple
		// installs on the same host. A configured-but-unusable override
		// (deleted/moved/non-executable) is logged and falls back to PATH
		// rather than registering a runtime that can't launch. When neither
		// the override nor PATH resolves, the profile is skipped (existing
		// behavior).
		var resolved string
		if override := strings.TrimSpace(d.cfg.ProfileCommandOverrides[profile.ID]); override != "" {
			if profilePathExecutable(override) {
				resolved = override
				d.logger.Info("custom runtime profile: using per-machine command path override",
					"workspace_id", workspaceID, "profile_id", profile.ID, "command_path", resolved)
			} else {
				d.logger.Warn("custom runtime profile: command path override not executable; falling back to PATH",
					"workspace_id", workspaceID, "profile_id", profile.ID,
					"override_path", override, "command_name", profile.CommandName)
			}
		}
		if resolved == "" {
			r, err := lookPath(profile.CommandName)
			if err != nil {
				// Host doesn't have this command — expected on hosts that aren't
				// provisioned for this profile. Skip without failing.
				d.logger.Info("skip custom runtime profile: command not found on PATH",
					"workspace_id", workspaceID, "profile_id", profile.ID,
					"command_name", profile.CommandName, "error", err)
				continue
			}
			resolved = r
		}
		// Best-effort version detection; an empty version is acceptable.
		version, verErr := detectAgentVersion(ctx, resolved)
		if verErr != nil {
			d.logger.Debug("custom runtime profile: version probe failed (registering with empty version)",
				"workspace_id", workspaceID, "profile_id", profile.ID, "path", resolved, "error", verErr)
			version = ""
		}
		displayName := profile.DisplayName
		if d.cfg.DeviceName != "" {
			displayName = fmt.Sprintf("%s (%s)", displayName, d.cfg.DeviceName)
		}
		d.recordProfileCommandPath(profile.ID, resolved)
		d.logger.Info("registering custom runtime profile",
			"workspace_id", workspaceID, "profile_id", profile.ID,
			"protocol_family", profile.ProtocolFamily, "command_path", resolved)
		// NOTE: profile.FixedArgs are launch args every agent on this runtime
		// inherits. Wiring them into the spawned command is intentionally not
		// done here — it's an optional, best-effort enhancement (see MUL-3284
		// PR2 task notes). TODO(MUL-3284): plumb FixedArgs into the agent
		// launch command if/when the agent backend exposes a hook for it.
		*runtimes = append(*runtimes, map[string]any{
			"name":       displayName,
			"type":       profile.ProtocolFamily,
			"version":    version,
			"status":     "online",
			"profile_id": profile.ID,
		})
	}
	return profileSetSignature(resp.RuntimeProfiles)
}

// profileSetSignature is a stable content hash of the workspace's custom
// runtime profile list (MUL-3332). The workspaceSyncLoop diffs this against
// the cached value on each tick: a mismatch means the user added, edited, or
// disabled a profile via the web UI / CLI between syncs and the daemon must
// re-register so the new runtime instance shows up in the list without a
// restart.
//
// The hashed projection covers exactly the fields that affect what the
// daemon sends in a Register call: ID, Enabled, ProtocolFamily, CommandName,
// FixedArgs (the launch args every agent on this runtime inherits) and
// Visibility (so a hypothetical future per-creator filter still triggers
// drift). Profiles are sorted by ID first so the digest is order-independent
// (the server is allowed to return them in any order).
func profileSetSignature(profiles []RuntimeProfile) string {
	if len(profiles) == 0 {
		return "0"
	}
	sorted := append([]RuntimeProfile(nil), profiles...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	h := fnv.New64a()
	// Field separator chosen to never appear in a UUID, slug, or arg.
	const sep = "\x1f"
	for _, p := range sorted {
		_, _ = fmt.Fprintf(h, "%s%s%t%s%s%s%s%s%s%s",
			p.ID, sep,
			p.Enabled, sep,
			p.ProtocolFamily, sep,
			p.CommandName, sep,
			p.Visibility, sep,
		)
		for _, a := range p.FixedArgs {
			_, _ = fmt.Fprintf(h, "%s%s", a, sep)
		}
		// Record list end so [a,b] and [ab] hash differently.
		_, _ = h.Write([]byte("\x1e"))
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

func newWorkspaceState(workspaceID string, runtimeIDs []string, reposVersion string, repos []RepoData, settings json.RawMessage) *workspaceState {
	return &workspaceState{
		workspaceID:     workspaceID,
		runtimeIDs:      runtimeIDs,
		reposVersion:    reposVersion,
		allowedRepoURLs: repoAllowlist(repos),
		settings:        settings,
	}
}

func repoAllowlist(repos []RepoData) map[string]struct{} {
	allowed := make(map[string]struct{}, len(repos))
	for _, repo := range repos {
		if repo.URL == "" {
			continue
		}
		allowed[repo.URL] = struct{}{}
	}
	return allowed
}

func (d *Daemon) setWorkspaceRepoSyncError(workspaceID, syncErr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ws, ok := d.workspaces[workspaceID]; ok {
		ws.lastRepoSyncErr = syncErr
	}
}

func (d *Daemon) workspaceRepoAllowed(workspaceID, repoURL string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		return false
	}
	if _, allowed := ws.allowedRepoURLs[repoURL]; allowed {
		return true
	}
	if _, allowed := ws.taskRepoURLs[repoURL]; allowed {
		return true
	}
	return false
}

func (d *Daemon) workspaceLastRepoSyncErr(workspaceID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		return ""
	}
	return ws.lastRepoSyncErr
}

// workspaceCoAuthoredByEnabled returns whether the Co-authored-by hook should
// be installed for the given workspace. Defaults to true when either setting
// is absent (new workspaces, older servers that don't send settings).
//
// The hook is gated by BOTH the GitHub master switch (`github_enabled`) and
// the dedicated co-author switch (`co_authored_by_enabled`) so flipping the
// workspace's master GitHub toggle off also stops new trailers from landing
// in commits, matching the contract documented in RFC MUL-2414 §4.8.
func (d *Daemon) workspaceCoAuthoredByEnabled(workspaceID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok || len(ws.settings) == 0 {
		return true // default: enabled
	}
	var s struct {
		GitHubEnabled       *bool `json:"github_enabled"`
		CoAuthoredByEnabled *bool `json:"co_authored_by_enabled"`
	}
	if err := json.Unmarshal(ws.settings, &s); err != nil {
		return true // default: enabled when payload is malformed
	}
	if s.GitHubEnabled != nil && !*s.GitHubEnabled {
		return false
	}
	if s.CoAuthoredByEnabled == nil {
		return true // default: enabled
	}
	return *s.CoAuthoredByEnabled
}

// registerTaskRepos merges task-scoped repos (e.g. project github_repo
// resources lifted into resp.Repos by the claim handler) into the workspace's
// allowlist and kicks off a cache sync for any URLs that aren't yet cached.
//
// It's safe to call with the workspace's own repos — duplicates are
// idempotent. Called from runTask before the agent spawns so
// `multica repo checkout` accepts project-only URLs without an extra round
// trip back to GetWorkspaceRepos (which doesn't carry project resources).
func (d *Daemon) registerTaskRepos(workspaceID string, repos []RepoData) {
	if len(repos) == 0 {
		return
	}

	type repoCandidate struct {
		url     string
		tracked bool
	}

	d.mu.Lock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		d.mu.Unlock()
		return
	}
	if ws.taskRepoURLs == nil {
		ws.taskRepoURLs = make(map[string]struct{}, len(repos))
	}
	candidates := make([]repoCandidate, 0, len(repos))
	for _, repo := range repos {
		url := strings.TrimSpace(repo.URL)
		if url == "" {
			continue
		}
		// Don't re-sync if the URL is already tracked (workspace or task-scoped)
		// AND the cache already has it.
		_, inWorkspace := ws.allowedRepoURLs[url]
		_, inTask := ws.taskRepoURLs[url]
		ws.taskRepoURLs[url] = struct{}{}
		candidates = append(candidates, repoCandidate{
			url:     url,
			tracked: inWorkspace || inTask,
		})
	}
	d.mu.Unlock()

	toSync := make([]RepoData, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.tracked && d.repoCache != nil && d.repoCache.Lookup(workspaceID, candidate.url) != "" {
			continue
		}
		toSync = append(toSync, RepoData{URL: candidate.url})
	}

	if d.repoCache != nil && len(toSync) > 0 {
		// Sync in the background — same shape used at workspace registration.
		// `ensureRepoReady` reports a meaningful error if the cache isn't ready
		// yet, so the agent's first checkout will surface a sync failure
		// without silently treating it as a config bug.
		d.bgSyncs.Add(1)
		go func() {
			defer d.bgSyncs.Done()
			d.syncWorkspaceRepos(workspaceID, toSync)
		}()
	}
}

// waitBackgroundSyncs blocks until every background sync started by
// registerTaskRepos has finished. Intended for test teardown: tests that
// hand the daemon a t.TempDir-backed repo cache must call this before
// returning, otherwise an in-flight clone/fetch can race against TempDir
// cleanup and surface as an unrelated "directory not empty" failure.
func (d *Daemon) waitBackgroundSyncs() {
	d.bgSyncs.Wait()
}

func (d *Daemon) syncWorkspaceRepos(workspaceID string, repos []RepoData) {
	if d.repoCache == nil {
		return
	}
	if err := d.repoCache.Sync(workspaceID, repoDataToInfo(repos)); err != nil {
		d.setWorkspaceRepoSyncError(workspaceID, err.Error())
		d.logger.Warn("repo cache sync failed", "workspace_id", workspaceID, "error", err)
		return
	}
	d.setWorkspaceRepoSyncError(workspaceID, "")
}

func (d *Daemon) refreshWorkspaceRepos(ctx context.Context, workspaceID string) (*WorkspaceReposResponse, error) {
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := d.client.GetWorkspaceRepos(refreshCtx, workspaceID)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	if ws, ok := d.workspaces[workspaceID]; ok {
		ws.reposVersion = resp.ReposVersion
		ws.allowedRepoURLs = repoAllowlist(resp.Repos)
		// Keep the cached settings in sync with the server. The daemon's
		// feature gates (e.g. workspaceCoAuthoredByEnabled) read directly from
		// this field, so toggling a Setting in the web UI must update it here
		// without requiring a daemon restart. An empty payload from the server
		// clears the override and falls back to defaults.
		ws.settings = resp.Settings
	}
	d.mu.Unlock()

	return resp, nil
}

// refreshWorkspaceRuntimeProfiles fetches the workspace's enabled custom
// runtime profile list (MUL-3332), compares its content signature against
// the value cached on the workspaceState, and triggers a re-register when
// the signature has drifted. This is the entry point that lets profiles
// added / edited / disabled via the web UI or CLI become visible in the
// runtime list within one workspaceSyncLoop tick instead of requiring a
// daemon restart.
//
// Best-effort: a fetch error (older server, network blip) is logged and
// swallowed — the cached signature is preserved so the next tick can still
// detect a real drift. A successfully-fetched-but-unchanged signature is the
// expected steady state and short-circuits without any further work.
//
// On drift the function takes a path that deliberately differs from
// reregisterWorkspaceAfterRuntimeGone in two ways:
//
//  1. It does NOT call RecoverOrphans for the returned runtime IDs. The
//     server's RecoverOrphanedTasksForRuntime hard-fails every
//     dispatched/running/waiting_local_directory task on a runtime, which is
//     the correct response when a runtime row was actually deleted server-
//     side, but a catastrophic false positive on profile drift: a built-in
//     runtime still actively executing tasks would have its work killed
//     just because the user added a sibling custom profile.
//
//  2. It tolerates ErrNoRuntimesToRegister (custom-only daemon disables its
//     only profile) by Deregistering the now-stale local runtime IDs and
//     clearing local tracking. Without this, registerRuntimesForWorkspace
//     would short-circuit on the empty list, the daemon would keep polling
//     and heartbeating runtimes that should be offline, and the server
//     would leave them online for the full 150 s stale-heartbeat window.
//
// The workspaceState pointer is never replaced (matches the invariant
// documented on syncWorkspacesFromAPI and reregisterWorkspaceAfterRuntimeGone).
func (d *Daemon) refreshWorkspaceRuntimeProfiles(ctx context.Context, workspaceID string) error {
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := d.client.GetRuntimeProfiles(refreshCtx, workspaceID)
	if err != nil {
		// Older server (no profiles route) returns 404; the daemon should not
		// log a noisy warning on every sync tick in that case.
		return err
	}
	var profiles []RuntimeProfile
	if resp != nil {
		profiles = resp.RuntimeProfiles
	}
	live := profileSetSignature(profiles)

	d.mu.Lock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		d.mu.Unlock()
		// Workspace was removed between sync ticks — nothing to do.
		return nil
	}
	cached := ws.profileSetSig
	d.mu.Unlock()

	if cached == live {
		return nil
	}

	d.logger.Info("custom runtime profile set changed; refreshing workspace runtimes",
		"workspace_id", workspaceID, "previous_sig", cached, "current_sig", live,
		"profile_count", len(profiles))

	regResp, profileSig, err := d.registerRuntimesForWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, ErrNoRuntimesToRegister) {
			// Convergence-to-zero: a custom-only daemon's only enabled
			// profile was just disabled / deleted, and there are no built-in
			// agents to fall back on. Drop the daemon's local tracking and
			// proactively Deregister the orphaned server-side rows so the
			// runtime list converges to empty without waiting on the 150 s
			// stale-heartbeat sweep.
			return d.convergeWorkspaceRuntimesToZero(ctx, workspaceID, profileSig)
		}
		return err
	}

	newIDs, droppedIDs, ok := d.applyRegisterResponseInPlace(workspaceID, regResp, profileSig)
	if !ok {
		return fmt.Errorf("workspace %s no longer tracked", workspaceID)
	}

	for _, rid := range newIDs {
		d.logger.Info("re-registered runtime after profile drift",
			"workspace_id", workspaceID, "runtime_id", rid)
	}
	d.notifyRuntimeSetChanged()

	// Drift may have shrunk the runtime set (a profile got disabled while
	// other runtimes survive). Eagerly mark those server-side rows offline
	// so the runtime list reflects reality immediately; a 5xx blip here is
	// fine because the server's stale-heartbeat sweep will pick them up
	// within ~150 s as a backstop.
	if len(droppedIDs) > 0 {
		if err := d.client.Deregister(ctx, droppedIDs); err != nil {
			d.logger.Warn("deregister of dropped runtimes after profile drift failed",
				"workspace_id", workspaceID, "runtime_ids", droppedIDs, "error", err)
		}
	}

	// Intentionally NO RecoverOrphans here: see method doc.
	return nil
}

// convergeWorkspaceRuntimesToZero handles the drift-refresh case where
// registerRuntimesForWorkspace would have short-circuited because the daemon
// has nothing to host on this workspace anymore. It Deregisters the
// previously-tracked runtime IDs (best-effort) and clears the daemon's local
// tracking so taskWakeup / heartbeat / poll loops stop attempting work
// against runtimes that should now be offline.
//
// The workspaceState pointer is preserved: the workspace itself is still a
// valid workspace the user belongs to, just one with no agents on this
// daemon for the moment. If the user re-enables a profile or installs a
// built-in agent, the next sync tick's profile-drift detection (or a daemon
// restart) will register it again.
func (d *Daemon) convergeWorkspaceRuntimesToZero(ctx context.Context, workspaceID, profileSig string) error {
	d.mu.Lock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		d.mu.Unlock()
		return nil
	}
	oldRuntimeIDs := append([]string(nil), ws.runtimeIDs...)
	for _, rid := range oldRuntimeIDs {
		delete(d.runtimeIndex, rid)
	}
	ws.runtimeIDs = nil
	if profileSig != "" {
		// Cache the converged-empty signature so we don't loop into
		// re-converging on every subsequent sync tick.
		ws.profileSetSig = profileSig
	}
	d.mu.Unlock()

	d.logger.Info("custom runtime profile drift converged to zero; clearing local tracking",
		"workspace_id", workspaceID, "deregistered_runtime_ids", oldRuntimeIDs)

	if len(oldRuntimeIDs) > 0 {
		if err := d.client.Deregister(ctx, oldRuntimeIDs); err != nil {
			// Best-effort: the server's stale-heartbeat sweep marks the rows
			// offline within ~150 s as a backstop, and on the daemon side
			// we have already stopped heartbeating them.
			d.logger.Warn("deregister after zero-runtime convergence failed",
				"workspace_id", workspaceID, "runtime_ids", oldRuntimeIDs, "error", err)
		}
	}
	d.notifyRuntimeSetChanged()
	return nil
}

func (d *Daemon) ensureRepoReady(ctx context.Context, workspaceID, repoURL string) error {
	if d.repoCache == nil {
		return fmt.Errorf("repo cache not initialized")
	}

	repoURL = strings.TrimSpace(repoURL)

	d.mu.Lock()
	ws, ok := d.workspaces[workspaceID]
	d.mu.Unlock()
	if !ok {
		return fmt.Errorf("workspace is not watched by this daemon: %s", workspaceID)
	}

	// Record whether the cache already had this repo before we took the
	// per-workspace mutex. The two states behave differently below:
	//
	//   - cacheHitOnEntry=true: the repo is already cloned; we still must
	//     refresh `workspaceState.settings` because the /repo/checkout
	//     handler reads workspaceCoAuthoredByEnabled right after this and
	//     the 30s workspaceSyncLoop tick is too slow for a freshly-flipped
	//     GitHub master switch / `co_authored_by_enabled` toggle to feel
	//     live (RFC MUL-2414 §4.8; PR #2847 review by Emacs).
	//
	//   - cacheHitOnEntry=false but cache hit *after* we acquire the mutex:
	//     a sibling goroutine on a concurrent cold-miss already refreshed
	//     and populated the cache. We can skip the duplicate refresh — the
	//     sibling's refresh is fresh enough for our gate read.
	cacheHitOnEntry := d.workspaceRepoAllowed(workspaceID, repoURL) && d.repoCache.Lookup(workspaceID, repoURL) != ""

	ws.repoRefreshMu.Lock()
	defer ws.repoRefreshMu.Unlock()

	if !cacheHitOnEntry && d.workspaceRepoAllowed(workspaceID, repoURL) && d.repoCache.Lookup(workspaceID, repoURL) != "" {
		return nil
	}

	resp, err := d.refreshWorkspaceRepos(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("refresh workspace repos: %w", err)
	}

	if !d.workspaceRepoAllowed(workspaceID, repoURL) {
		return ErrRepoNotConfigured
	}

	if d.repoCache.Lookup(workspaceID, repoURL) != "" {
		return nil
	}

	d.syncWorkspaceRepos(workspaceID, resp.Repos)

	if d.repoCache.Lookup(workspaceID, repoURL) != "" {
		return nil
	}

	if syncErr := d.workspaceLastRepoSyncErr(workspaceID); syncErr != "" {
		return fmt.Errorf("repo is configured but not synced: %s", syncErr)
	}

	return fmt.Errorf("repo is configured but not synced")
}

// DefaultTokenRenewalInterval is how often the daemon asks the server to
// extend its PAT. The server-side threshold is 7 days of remaining lifetime;
// polling every ~3 days gives at least two chances to renew before the
// window closes, so a single failed call (network blip, server restart) does
// not push the token out of the renewal window.
const DefaultTokenRenewalInterval = 3 * 24 * time.Hour

// preflightAuth runs the two auth-sensitive startup steps in their
// required order: a synchronous PAT renewal first, then the initial
// workspace sync. The order matters — running tryRenewToken before any
// other API call is what surfaces a user-actionable "run multica login"
// WARN when the PAT is already revoked or expired. If we let the
// workspace sync go first, its 401 would short-circuit Run before the
// renewal loop's first tick ever fires, and the operator would see only
// a generic auth failure in the workspace-sync log with no hint that
// re-login is the fix.
//
// The renewal is best-effort: tryRenewToken logs and returns, never
// propagating errors. preflightAuth's exit status is driven entirely by
// the workspace sync — so a transient renewal failure (network blip,
// 500) does not by itself block startup. A successful sync with zero
// workspaces is fine: a newly-signed-up user may start the daemon
// before creating their first workspace, and workspaceSyncLoop will
// register runtimes once one appears.
func (d *Daemon) preflightAuth(ctx context.Context) error {
	d.tryRenewToken(ctx)
	return d.syncWorkspacesFromAPI(ctx)
}

// tokenRenewalLoop keeps the daemon's PAT alive by periodically asking the
// server to extend its expires_at in-place. The startup renewal happens
// synchronously in preflightAuth so a daemon coming back online after a
// week of downtime gets a fresh expiry before its next heartbeat could
// 401; this loop owns the long-running ~3-day cadence after that.
//
// The server is authoritative on the renewal threshold (it sees expires_at;
// we don't), so this loop is intentionally dumb: call, log, sleep, repeat.
// On 401 we surface a clear "re-login required" warning because the daemon
// has no way to recover automatically — but we keep the loop running so the
// user sees the same warning on every cycle until they fix it, rather than
// silently exiting and forcing them to read scrollback to find the cause.
func (d *Daemon) tokenRenewalLoop(ctx context.Context) {
	ticker := time.NewTicker(DefaultTokenRenewalInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tryRenewToken(ctx)
		}
	}
}

// tryRenewToken performs one renewal round-trip with a short, isolated
// timeout. Errors are logged but never propagated — there is no caller to
// handle them. Failures are debug-level except for 401, which gets a
// user-actionable warning.
func (d *Daemon) tryRenewToken(ctx context.Context) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resp, err := d.client.RenewToken(reqCtx)
	if err != nil {
		if isUnauthorizedError(err) {
			loginHint := "'multica login'"
			if d.cfg.Profile != "" {
				loginHint = fmt.Sprintf("'multica login --profile %s'", d.cfg.Profile)
			}
			d.logger.Warn("auth token rejected by server — run "+loginHint+" to re-authenticate, then restart the daemon", "error", err)
			return
		}
		d.logger.Debug("token renewal failed; will retry on next cycle", "error", err)
		return
	}
	if resp.Renewed {
		d.logger.Info("auth token renewed", "expires_at", resp.ExpiresAt)
	} else {
		d.logger.Debug("auth token not yet eligible for renewal", "expires_at", resp.ExpiresAt)
	}
}

// workspaceSyncLoop periodically fetches the user's workspaces from the API
// and registers runtimes for any new ones.
