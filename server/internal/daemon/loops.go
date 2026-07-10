package daemon

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

func (d *Daemon) workspaceSyncLoop(ctx context.Context) {
	ticker := time.NewTicker(DefaultWorkspaceSyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.syncWorkspacesFromAPI(ctx); err != nil {
				d.logger.Debug("workspace sync failed", "error", err)
			}
		}
	}
}

// syncWorkspacesFromAPI fetches all workspaces the user belongs to and
// registers runtimes for any that aren't already tracked. Workspaces the user
// has left are cleaned up.
func (d *Daemon) syncWorkspacesFromAPI(ctx context.Context) error {
	d.reloading.Lock()
	defer d.reloading.Unlock()

	apiCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	workspaces, err := d.client.ListWorkspaces(apiCtx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	d.logger.Debug("workspace sync: fetched workspaces", "count", len(workspaces))

	apiIDs := make(map[string]string, len(workspaces)) // id -> name
	for _, ws := range workspaces {
		apiIDs[ws.ID] = ws.Name
	}

	d.mu.Lock()
	currentIDs := make(map[string]bool, len(d.workspaces))
	for id := range d.workspaces {
		currentIDs[id] = true
	}
	d.mu.Unlock()

	var registered int
	var removed int
	for id, name := range apiIDs {
		if currentIDs[id] {
			// Already tracked: refresh the cached workspace settings so
			// feature toggles flipped in the web UI take effect on the next
			// gated operation without a daemon restart (see RFC MUL-2414 §4.8;
			// reviewed in PR #2847). refreshWorkspaceRepos covers settings +
			// repos in a single round trip.
			if _, err := d.refreshWorkspaceRepos(ctx, id); err != nil {
				d.logger.Debug("workspace sync: refresh settings failed", "workspace_id", id, "error", err)
			}
			// Pick up custom runtime profiles created/edited/disabled via
			// the web UI or CLI between sync ticks (MUL-3332). Without this,
			// a profile added on the server would only become a runtime row
			// after a daemon restart or a runtime_gone recovery, because the
			// already-tracked branch never re-runs registerRuntimesForWorkspace
			// otherwise. refreshWorkspaceRuntimeProfiles is best-effort and
			// only re-registers when it observes a real signature drift, so
			// quiet workspaces incur exactly one cheap GetRuntimeProfiles
			// round trip per sync tick.
			if err := d.refreshWorkspaceRuntimeProfiles(ctx, id); err != nil {
				d.logger.Debug("workspace sync: profile refresh failed", "workspace_id", id, "error", err)
			}
			// Only intervene further if the workspace lost all of its
			// runtimes (most commonly because handleRuntimeGone pruned them
			// and its inline re-register failed). The pointer is not replaced
			// here either — ensureRepoReady holds repoRefreshMu from the
			// original pointer.
			if !d.workspaceNeedsRuntimeRecovery(id) {
				continue
			}
			d.logger.Info("workspace has no runtimes; retrying registration", "workspace_id", id, "name", name)
			if err := d.reregisterWorkspaceAfterRuntimeGone(ctx, id); err != nil {
				d.logger.Warn("retry register failed", "workspace_id", id, "error", err)
				continue
			}
			registered++
			continue
		}
		resp, profileSig, err := d.registerRuntimesForWorkspace(ctx, id)
		if err != nil {
			d.logger.Error("failed to register runtimes", "workspace_id", id, "name", name, "error", err)
			continue
		}
		runtimeIDs := make([]string, len(resp.Runtimes))
		for i, rt := range resp.Runtimes {
			runtimeIDs[i] = rt.ID
			d.logger.Info("registered runtime", "workspace_id", id, "runtime_id", rt.ID, "provider", rt.Provider)
		}
		d.mu.Lock()
		ws := newWorkspaceState(id, runtimeIDs, resp.ReposVersion, resp.Repos, resp.Settings)
		// Seed the profile signature so the next sync tick can detect drift
		// without re-registering on a transient fetch failure (empty sig is
		// the explicit "unknown — keep the previous value" sentinel from
		// appendProfileRuntimes; on first registration there is no previous
		// value, so empty stays empty).
		ws.profileSetSig = profileSig
		d.workspaces[id] = ws
		for _, rt := range resp.Runtimes {
			d.runtimeIndex[rt.ID] = rt
		}
		d.mu.Unlock()

		if d.repoCache != nil && len(resp.Repos) > 0 {
			go d.syncWorkspaceRepos(id, resp.Repos)
		}

		// Tell the server about any tasks the previous daemon process was
		// running on these runtimes. Without this, an issue can stay stuck
		// at in_progress until the slow heartbeat sweeper or the in-flight
		// task timeout (2.5h) kicks in.
		for _, rid := range runtimeIDs {
			if err := d.client.RecoverOrphans(ctx, rid); err != nil {
				d.logger.Warn("recover-orphans failed", "runtime_id", rid, "error", err)
			}
		}

		d.logger.Info("watching workspace", "workspace_id", id, "name", name, "runtimes", len(resp.Runtimes), "repos", len(resp.Repos))
		registered++
	}

	// Remove workspaces the user no longer belongs to.
	for id := range currentIDs {
		if _, ok := apiIDs[id]; !ok {
			d.mu.Lock()
			if ws, exists := d.workspaces[id]; exists {
				for _, rid := range ws.runtimeIDs {
					delete(d.runtimeIndex, rid)
				}
			}
			delete(d.workspaces, id)
			d.mu.Unlock()
			d.logger.Info("stopped watching workspace", "workspace_id", id)
			removed++
		}
	}
	if registered > 0 || removed > 0 {
		d.notifyRuntimeSetChanged()
	}

	if len(d.allRuntimeIDs()) == 0 && registered == 0 && len(workspaces) > 0 {
		return fmt.Errorf("failed to register runtimes for any of the %d workspace(s)", len(workspaces))
	}
	if registered > 0 || removed > 0 {
		d.logger.Debug("workspace sync done", "registered", registered, "removed", removed, "tracked", len(apiIDs))
	}
	return nil
}

// heartbeatLoop supervises per-runtime HTTP heartbeat goroutines. Each runtime
// gets an independent ticker so a slow heartbeat for one runtime cannot block
// heartbeats for any other runtime — this matters when a single daemon serves
// multiple workspaces, because the previous shared loop would serialize an
// up-to-30s HTTP timeout across every runtime in the set.
func (d *Daemon) heartbeatLoop(ctx context.Context) {
	runtimeSetCh, unsub := d.runtimeSet.Subscribe()
	defer unsub()

	cancels := make(map[string]context.CancelFunc)
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()

	sync := func() {
		want := make(map[string]struct{})
		for _, rid := range d.allRuntimeIDs() {
			want[rid] = struct{}{}
		}
		for rid, cancel := range cancels {
			if _, ok := want[rid]; !ok {
				cancel()
				delete(cancels, rid)
			}
		}
		for rid := range want {
			if _, ok := cancels[rid]; ok {
				continue
			}
			rctx, rcancel := context.WithCancel(ctx)
			cancels[rid] = rcancel
			go d.runRuntimeHeartbeat(rctx, rid)
		}
	}

	sync()
	for {
		select {
		case <-ctx.Done():
			return
		case <-runtimeSetCh:
			sync()
		}
	}
}

// runRuntimeHeartbeat owns the HTTP heartbeat schedule for a single runtime.
// The first tick fires after a small jittered delay (up to one full interval)
// to avoid a thundering herd when the daemon registers many runtimes at once.
func (d *Daemon) runRuntimeHeartbeat(ctx context.Context, rid string) {
	interval := d.cfg.HeartbeatInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	// Jittered initial delay; cap at the interval so the first beat still
	// happens within one period.
	if jitter := time.Duration(rand.Int63n(int64(interval))); jitter > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter):
		}
	}

	d.runHeartbeatTick(ctx, rid)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.runHeartbeatTick(ctx, rid)
		}
	}
}

func (d *Daemon) runHeartbeatTick(ctx context.Context, rid string) {
	// Keep the HTTP heartbeat even when the WebSocket heartbeat is healthy.
	// The heartbeat response is also the delivery lane for async runtime
	// actions such as model-list and local-skill discovery. Suppressing HTTP
	// after a WS ack made those UI-triggered requests dependent on the next WS
	// heartbeat and, when the wakeup stream missed a pending action, left them
	// pending until the frontend timed out. Duplicate liveness writes are less
	// costly than wedging those request/response flows.
	d.logger.Debug("heartbeat: HTTP tick", "runtime_id", rid)
	resp, err := d.client.SendHeartbeat(ctx, rid, nil)
	if err != nil {
		if ctx.Err() == nil {
			if isRuntimeNotFoundError(err) {
				// Server says this runtime is gone — recover instead of
				// looping on the dead UUID. handleRuntimeGone coalesces
				// concurrent callers and runs the recovery HTTP call under
				// the daemon root context so notifyRuntimeSetChanged
				// tearing down this heartbeat goroutine cannot abort it.
				go d.handleRuntimeGone(rid)
				return
			}
			d.logger.Warn("heartbeat failed", "runtime_id", rid, "error", err)
		}
		return
	}
	if resp != nil && resp.RuntimeGone {
		// The WS path returns a successful ack with RuntimeGone=true for the
		// same scenario; treat it the same way here in case HTTP starts
		// surfacing this signal too.
		go d.handleRuntimeGone(rid)
		return
	}
	d.handleHeartbeatActions(ctx, rid, resp)
}

// handleHeartbeatActions dispatches the pending-action set returned by either
// transport (HTTP POST /api/daemon/heartbeat or WS daemon:heartbeat_ack).
// Each action is dispatched in its own goroutine so a slow handler cannot
// block subsequent heartbeats.
func (d *Daemon) handleHeartbeatActions(ctx context.Context, runtimeID string, resp *HeartbeatResponse) {
	if resp == nil {
		return
	}
	if resp.PendingModelList != nil || resp.PendingLocalSkills != nil || resp.PendingLocalSkillImport != nil {
		d.logger.Debug("heartbeat: pending actions",
			"runtime_id", runtimeID,
			"model_list", resp.PendingModelList != nil,
			"local_skills", resp.PendingLocalSkills != nil,
			"local_skill_import", resp.PendingLocalSkillImport != nil,
		)
	}
	if resp.PendingModelList != nil {
		if rt := d.findRuntime(runtimeID); rt != nil {
			go d.handleModelList(ctx, *rt, resp.PendingModelList.ID)
		}
	}
	if resp.PendingLocalSkills != nil {
		if rt := d.findRuntime(runtimeID); rt != nil {
			go d.handleLocalSkillList(ctx, *rt, resp.PendingLocalSkills.ID)
		}
	}
	// Prefer the batch field (new backend); fall back to singular (old backend).
	if len(resp.PendingLocalSkillImports) > 0 {
		if rt := d.findRuntime(runtimeID); rt != nil {
			for _, imp := range resp.PendingLocalSkillImports {
				go d.handleLocalSkillImport(ctx, *rt, imp)
			}
		}
	} else if resp.PendingLocalSkillImport != nil {
		if rt := d.findRuntime(runtimeID); rt != nil {
			go d.handleLocalSkillImport(ctx, *rt, *resp.PendingLocalSkillImport)
		}
	}
}

// handleModelList resolves the provider's supported models (via static
// catalog or by shelling out to the agent CLI) and reports the result
// back to the server. Model discovery failures are reported as empty
// lists rather than errors so the UI can still render a creatable
// dropdown.
func (d *Daemon) handleModelList(ctx context.Context, rt Runtime, requestID string) {
	d.logger.Info("model list requested", "runtime_id", rt.ID, "request_id", requestID, "provider", rt.Provider)

	entry, ok := d.cfg.Agents[rt.Provider]
	if !ok {
		d.reportModelListResult(ctx, rt, requestID, map[string]any{
			"status": "failed",
			"error":  fmt.Sprintf("no agent configured for provider %q", rt.Provider),
		})
		return
	}

	models, err := agent.ListModels(ctx, rt.Provider, entry.Path)
	if err != nil {
		d.reportModelListResult(ctx, rt, requestID, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return
	}

	// Wire format matches handler.ModelEntry. Use a struct (not
	// map[string]string) so the Default bool and the per-model
	// Thinking catalog round-trip — without it the UI loses its
	// "default" badge on the advertised pick and the thinking-level
	// picker for claude/codex (MUL-2339).
	type thinkingLevelWire struct {
		Value       string `json:"value"`
		Label       string `json:"label"`
		Description string `json:"description,omitempty"`
	}
	type modelThinkingWire struct {
		SupportedLevels []thinkingLevelWire `json:"supported_levels"`
		DefaultLevel    string              `json:"default_level,omitempty"`
	}
	type modelWire struct {
		ID       string             `json:"id"`
		Label    string             `json:"label"`
		Provider string             `json:"provider,omitempty"`
		Default  bool               `json:"default,omitempty"`
		Thinking *modelThinkingWire `json:"thinking,omitempty"`
	}
	wire := make([]modelWire, 0, len(models))
	for _, m := range models {
		entry := modelWire{
			ID:       m.ID,
			Label:    m.Label,
			Provider: m.Provider,
			Default:  m.Default,
		}
		if m.Thinking != nil {
			levels := make([]thinkingLevelWire, 0, len(m.Thinking.SupportedLevels))
			for _, lvl := range m.Thinking.SupportedLevels {
				levels = append(levels, thinkingLevelWire{
					Value:       lvl.Value,
					Label:       lvl.Label,
					Description: lvl.Description,
				})
			}
			entry.Thinking = &modelThinkingWire{
				SupportedLevels: levels,
				DefaultLevel:    m.Thinking.DefaultLevel,
			}
		}
		wire = append(wire, entry)
	}
	d.reportModelListResult(ctx, rt, requestID, map[string]any{
		"status":    "completed",
		"models":    wire,
		"supported": agent.ModelSelectionSupported(rt.Provider),
	})
}

func (d *Daemon) handleLocalSkillList(ctx context.Context, rt Runtime, requestID string) {
	d.logger.Info("runtime local skills requested", "runtime_id", rt.ID, "request_id", requestID, "provider", rt.Provider)

	skills, supported, err := listRuntimeLocalSkills(rt.Provider)
	if err != nil {
		d.reportLocalSkillListResult(ctx, rt, requestID, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return
	}

	d.reportLocalSkillListResult(ctx, rt, requestID, map[string]any{
		"status":    "completed",
		"skills":    skills,
		"supported": supported,
	})
}

func (d *Daemon) handleLocalSkillImport(ctx context.Context, rt Runtime, pending PendingLocalSkillImport) {
	d.logger.Info("runtime local skill import requested", "runtime_id", rt.ID, "request_id", pending.ID, "provider", rt.Provider, "skill_key", pending.SkillKey)

	skill, supported, err := loadRuntimeLocalSkillBundle(rt.Provider, pending.SkillKey)
	if err != nil {
		d.reportLocalSkillImportResult(ctx, rt, pending.ID, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return
	}
	if !supported {
		d.reportLocalSkillImportResult(ctx, rt, pending.ID, map[string]any{
			"status": "failed",
			"error":  fmt.Sprintf("provider %q does not expose runtime local skills", rt.Provider),
		})
		return
	}

	d.reportLocalSkillImportResult(ctx, rt, pending.ID, map[string]any{
		"status": "completed",
		"skill":  skill,
	})
}

// runtimeReportBackoffs defines the retry schedule for delivering any
// daemon→server async result (model list, local-skill list, local-skill
// import). First attempt runs immediately, then we back off. The sum
// (≈6.5s) stays well under the server-side running timeout (60s) so a
// report that eventually lands still updates the request instead of
// racing a timeout transition.
//
// Overridable for tests to avoid real sleeps.
var runtimeReportBackoffs = []time.Duration{0, 500 * time.Millisecond, 2 * time.Second, 4 * time.Second}

// reportLocalSkillListResult delivers a list-report to the server with retry
// on transient failures. See reportRuntimeResultWithRetry for semantics.
func (d *Daemon) reportLocalSkillListResult(ctx context.Context, rt Runtime, requestID string, payload map[string]any) {
	d.reportRuntimeResultWithRetry(ctx, "local_skill_list", rt.ID, requestID, func(ctx context.Context) error {
		return d.client.ReportLocalSkillListResult(ctx, rt.ID, requestID, payload)
	})
}

// reportLocalSkillImportResult delivers an import-report to the server with
// retry on transient failures.
func (d *Daemon) reportLocalSkillImportResult(ctx context.Context, rt Runtime, requestID string, payload map[string]any) {
	d.reportRuntimeResultWithRetry(ctx, "local_skill_import", rt.ID, requestID, func(ctx context.Context) error {
		return d.client.ReportLocalSkillImportResult(ctx, rt.ID, requestID, payload)
	})
}

// reportModelListResult delivers a model-list report to the server with retry
// on transient failures. Without this the daemon used to fire once and
// swallow any 5xx, leaving the request stranded in "running" on the server
// until its 60s timeout — defeating the multi-node store fix.
func (d *Daemon) reportModelListResult(ctx context.Context, rt Runtime, requestID string, payload map[string]any) {
	d.reportRuntimeResultWithRetry(ctx, "model_list", rt.ID, requestID, func(ctx context.Context) error {
		return d.client.ReportModelListResult(ctx, rt.ID, requestID, payload)
	})
}

// reportRuntimeResultWithRetry retries `fn` on 5xx / network errors and
// stops on success, 4xx, or after exhausting runtimeReportBackoffs.
//
// Why this exists: the server persists the report through a Redis / DB
// write; on a transient store failure it correctly returns 500. Without a
// client-side retry the daemon would fire once, swallow the error, and the
// pending request stays in "running" on the server until its timeout — which
// is exactly the "daemon did not respond" failure mode the multi-node store
// fix was meant to eliminate. 4xx is treated as permanent (request-not-found,
// cross-workspace token rejected, bad body) — retrying those just wastes
// heartbeat cycles.
func (d *Daemon) reportRuntimeResultWithRetry(ctx context.Context, kind, runtimeID, requestID string, fn func(context.Context) error) {
	var lastErr error
	for attempt, wait := range runtimeReportBackoffs {
		if wait > 0 {
			select {
			case <-ctx.Done():
				d.logger.Error("runtime async report cancelled",
					"kind", kind, "runtime_id", runtimeID, "request_id", requestID,
					"attempt", attempt, "error", ctx.Err())
				return
			case <-time.After(wait):
			}
		}
		err := fn(ctx)
		if err == nil {
			if attempt > 0 {
				d.logger.Info("runtime async report succeeded after retry",
					"kind", kind, "runtime_id", runtimeID, "request_id", requestID,
					"attempt", attempt+1)
			}
			return
		}
		lastErr = err

		// 4xx is permanent (request expired, workspace mismatch, malformed
		// body). No amount of retrying will make it succeed.
		var reqErr *requestError
		if errors.As(err, &reqErr) && reqErr.StatusCode >= 400 && reqErr.StatusCode < 500 {
			d.logger.Error("runtime async report rejected — not retrying",
				"kind", kind, "runtime_id", runtimeID, "request_id", requestID,
				"status", reqErr.StatusCode, "error", err)
			return
		}

		d.logger.Warn("runtime async report failed — will retry",
			"kind", kind, "runtime_id", runtimeID, "request_id", requestID,
			"attempt", attempt+1, "error", err)
	}
	d.logger.Error("runtime async report exhausted retries",
		"kind", kind, "runtime_id", runtimeID, "request_id", requestID, "error", lastErr)
}

// pollLoop supervises one runtimePoller goroutine per registered runtime,
// fans wake-up signals out to all of them, and waits for in-flight tasks to
// drain on shutdown. Per-runtime workers replace the previous round-robin
// loop so that a slow ClaimTask call (HTTP 30s timeout) for one runtime no
// longer delays claims on every other runtime — that was the cross-workspace
// stall mode reported in MUL-1744.
func (d *Daemon) pollLoop(ctx context.Context, taskWakeups <-chan taskWakeup) error {
	sem := newTaskSlotSemaphore(d.cfg.MaxConcurrentTasks)
	var taskWG sync.WaitGroup   // tracks in-flight handleTask goroutines
	var pollerWG sync.WaitGroup // tracks runRuntimePoller goroutines

	runtimeSetCh, unsub := d.runtimeSet.Subscribe()
	defer unsub()

	type pollerHandle struct {
		cancel context.CancelFunc
		wakeup chan struct{}
	}
	pollers := make(map[string]*pollerHandle)

	syncPollers := func() {
		want := make(map[string]struct{})
		for _, rid := range d.allRuntimeIDs() {
			want[rid] = struct{}{}
		}
		for rid, h := range pollers {
			if _, ok := want[rid]; !ok {
				h.cancel()
				delete(pollers, rid)
			}
		}
		for rid := range want {
			if _, ok := pollers[rid]; ok {
				continue
			}
			pctx, pcancel := context.WithCancel(ctx)
			wakeup := make(chan struct{}, 1)
			pollers[rid] = &pollerHandle{cancel: pcancel, wakeup: wakeup}
			pollerWG.Add(1)
			go func(rid string, pctx context.Context, wakeup <-chan struct{}) {
				defer pollerWG.Done()
				d.runRuntimePoller(pctx, ctx, rid, sem, wakeup, &taskWG)
			}(rid, pctx, wakeup)
		}
	}

	syncPollers()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("poll loop stopping, waiting for in-flight tasks", "max_wait", "30s")
			for _, h := range pollers {
				h.cancel()
			}
			// Wait for all pollers to fully return before waiting on taskWG.
			// Otherwise a poller that's between ClaimTask and taskWG.Add(1)
			// could race with taskWG.Wait when the counter is zero, which
			// is an undefined sync.WaitGroup misuse.
			pollerWG.Wait()

			waitDone := make(chan struct{})
			go func() { taskWG.Wait(); close(waitDone) }()
			select {
			case <-waitDone:
			case <-time.After(30 * time.Second):
				d.logger.Warn("timed out waiting for in-flight tasks")
			}
			return ctx.Err()
		case <-runtimeSetCh:
			syncPollers()
		case wakeup := <-taskWakeups:
			if wakeup.runtimeID != "" {
				if h, ok := pollers[wakeup.runtimeID]; ok {
					d.logger.Debug("task wakeup: signaling runtime poller", "runtime_id", wakeup.runtimeID)
					select {
					case h.wakeup <- struct{}{}:
					default:
					}
				} else {
					d.logger.Debug("task wakeup: runtime poller not found", "runtime_id", wakeup.runtimeID, "pollers", len(pollers))
				}
				continue
			}

			// A wakeup without a runtime_id is a catch-up signal (for example,
			// immediately after the websocket connects). Fan it out so queued
			// work that existed before the connection is still discovered.
			d.logger.Debug("task wakeup: fanning out to pollers", "pollers", len(pollers))
			for _, h := range pollers {
				select {
				case h.wakeup <- struct{}{}:
				default:
				}
			}
		}
	}
}

// runRuntimePoller is the per-runtime claim+dispatch loop. It owns its own
// poll cadence and wakeup channel so that a slow HTTP claim for this runtime
// cannot delay any other runtime's claims.
//
// The execution slot is acquired BEFORE ClaimTask. The alternative —
// claiming first and then waiting for a slot — would let claimed tasks pile
// up in the server-side `dispatched` state without a corresponding
// StartTask, and the server's sweeper would fail them as `failed/timeout`
// after dispatchTimeoutSeconds=300s (runtime_sweeper.go:25). That is the
// exact user-visible failure this issue is fixing, so we cannot risk
// recreating it under load.
//
// Slot-before-claim does mean a slow claim holds a slot during its HTTP
// roundtrip; the upper bound is `client.Timeout = 30s` (client.go:59), well
// below the 300s dispatch timeout, so other runtimes' tasks stay in
// server-side `queued` state (which has no timeout) rather than entering
// `dispatched` and racing the sweeper.
//
// pollerCtx is cancelled when this runtime is removed from the watched set
// (e.g. workspace de-registered). parentCtx is the daemon's root ctx and is
// passed to handleTask so an in-flight task is not killed just because the
// runtime set changed mid-flight — the task continues to run until the
// daemon itself shuts down (or the server cancels it).
func (d *Daemon) runRuntimePoller(
	pollerCtx, parentCtx context.Context,
	rid string,
	sem chan int,
	wakeup <-chan struct{},
	taskWG *sync.WaitGroup,
) {
	if offset := runtimePollOffset(rid, d.cfg.PollInterval); offset > 0 {
		d.logger.Debug("poll: initial offset", "runtime_id", rid, "offset", offset)
		if err := sleepWithContextOrWakeup(pollerCtx, offset, wakeup); err != nil {
			return
		}
	}

	for {
		if pollerCtx.Err() != nil {
			return
		}

		if delayed, err := d.waitForRuntimeTaskSpacing(pollerCtx, rid, wakeup); err != nil {
			return
		} else if delayed {
			continue
		}

		// Acquire an execution slot before claiming. If at capacity, sleep
		// without claiming so we don't push a task into `dispatched` and
		// then race the 5-min server-side dispatch timeout while waiting.
		slot, acquired, woke, err := waitForTaskSlot(pollerCtx, sem, wakeup, taskSlotWaitTimeout)
		if err != nil {
			return
		}
		if !acquired {
			d.logger.Debug("poll: at capacity", "runtime_id", rid, "running", d.cfg.MaxConcurrentTasks)
			if woke {
				continue
			}
			if err := sleepWithContextOrWakeup(pollerCtx, capacityBackoff(d.cfg.PollInterval), wakeup); err != nil {
				return
			}
			continue
		}

		task, err := d.client.ClaimTask(pollerCtx, rid)
		if err != nil {
			sem <- slot
			if pollerCtx.Err() == nil {
				if isRuntimeNotFoundError(err) {
					// Server says this runtime is gone — recover and exit
					// the poller; the runtime-set watcher will tear this
					// goroutine down via pollerCtx once the workspace is
					// re-registered with a new runtime ID.
					go d.handleRuntimeGone(rid)
					return
				}
				d.logger.Warn("claim task failed", "runtime_id", rid, "error", err)
			}
			if err := sleepWithContextOrWakeup(pollerCtx, d.cfg.PollInterval, wakeup); err != nil {
				return
			}
			continue
		}

		if task == nil {
			sem <- slot
			if err := sleepWithContextOrWakeup(pollerCtx, d.cfg.PollInterval, wakeup); err != nil {
				return
			}
			continue
		}
		d.recordRuntimeTaskStart(rid)

		taskTarget := task.IssueID
		if taskTarget == "" && task.ChatSessionID != "" {
			taskTarget = "chat:" + shortID(task.ChatSessionID)
		}
		d.logger.Info("task received", "task", shortID(task.ID), "target", taskTarget)
		taskWG.Add(1)
		d.activeTasks.Add(1)
		go func(t Task, slot int) {
			defer taskWG.Done()
			defer d.activeTasks.Add(-1)
			defer func() { sem <- slot }()
			d.handleTask(parentCtx, t, slot)
		}(*task, slot)
		// Loop immediately: more tasks may already be queued for this runtime.
	}
}

func (d *Daemon) waitForRuntimeTaskSpacing(ctx context.Context, runtimeID string, wakeup <-chan struct{}) (bool, error) {
	if d.cfg.CodexMinTaskInterval <= 0 {
		return false, nil
	}
	d.mu.Lock()
	rt := d.runtimeIndex[runtimeID]
	last := d.runtimeLastTaskStart[runtimeID]
	d.mu.Unlock()
	if rt.Provider != "codex" || last.IsZero() {
		return false, nil
	}
	wait := d.cfg.CodexMinTaskInterval - time.Since(last)
	if wait <= 0 {
		return false, nil
	}
	d.logger.Debug("poll: codex runtime spacing active", "runtime_id", runtimeID, "wait", wait)
	if err := sleepWithContextOrWakeup(ctx, wait, wakeup); err != nil {
		return false, err
	}
	return true, nil
}

func (d *Daemon) recordRuntimeTaskStart(runtimeID string) {
	if d.cfg.CodexMinTaskInterval <= 0 || runtimeID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runtimeLastTaskStart == nil {
		d.runtimeLastTaskStart = make(map[string]time.Time)
	}
	d.runtimeLastTaskStart[runtimeID] = time.Now()
}

func runtimePollOffset(runtimeID string, interval time.Duration) time.Duration {
	if interval <= 0 || runtimeID == "" {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(runtimeID))
	return time.Duration(h.Sum64() % uint64(interval))
}

func capacityBackoff(pollInterval time.Duration) time.Duration {
	if pollInterval <= 0 || pollInterval > taskSlotCapacityBackoff {
		return taskSlotCapacityBackoff
	}
	return pollInterval
}

func waitForTaskSlot(ctx context.Context, sem chan int, wakeup <-chan struct{}, wait time.Duration) (slot int, acquired, woke bool, err error) {
	select {
	case slot = <-sem:
		return slot, true, false, nil
	case <-ctx.Done():
		return 0, false, false, ctx.Err()
	default:
	}

	if wait <= 0 {
		return 0, false, false, nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case slot = <-sem:
		return slot, true, false, nil
	case <-wakeup:
		return 0, false, true, nil
	case <-ctx.Done():
		return 0, false, false, ctx.Err()
	case <-timer.C:
		return 0, false, false, nil
	}
}

// newTaskSlotSemaphore returns a buffered channel pre-populated with stable
// slot indices [0, n). Receive to acquire a slot, send the same slot back to
// release. Used by pollLoop to expose MULTICA_TASK_SLOT to spawned tasks.
func newTaskSlotSemaphore(maxConcurrentTasks int) chan int {
	sem := make(chan int, maxConcurrentTasks)
	for i := 0; i < maxConcurrentTasks; i++ {
		sem <- i
	}
	return sem
}

// shouldInterruptAgent decides whether the running agent should be cancelled
// based on the latest GetTaskStatus call. Pure function so the decision is
// trivially testable; the polling goroutine in watchTaskCancellation is just
// I/O around it.
//
// Two conditions trigger cancellation:
//
//  1. status is a terminal state — "completed", "failed", or "cancelled"
//     (isAgentTaskTerminal). The server has already finalized the task: user
//     cancel, issue reassignment, the runtime offline sweeper flipping
//     running → failed during a disconnect, or a duplicate execution that
//     already completed it. Letting the local agent run on is pure waste —
//     CompleteAgentTask only accepts status == "running", so its eventual
//     CompleteTask/FailTask callback is guaranteed to fail and just adds log
//     noise. Reusing isAgentTaskTerminal keeps this set in lockstep with the
//     GC's notion of a terminal task.
//  2. err is a 404 with "task not found" — the task row was deleted while
//     the agent was running. Without this we'd let the local agent keep
//     emitting tool calls against a dead task for its full timeout window.
//
// All other errors (transient network, 5xx, ...) intentionally do NOT
// trigger cancellation — the next tick will retry and we don't want a
// flaky link to kill an in-flight agent.
func shouldInterruptAgent(status string, err error) bool {
	if err != nil {
		return isTaskNotFoundError(err)
	}
	return isAgentTaskTerminal(status)
}

// watchTaskCancellation polls the server for the task's status on the given
// interval and returns a channel that is closed when the running agent
// should be interrupted. The polling goroutine stops when ctx is cancelled,
// so callers should pass the runCtx that was set up around the agent run.
func (d *Daemon) watchTaskCancellation(ctx context.Context, taskID string, pollInterval time.Duration, taskLog *slog.Logger) <-chan struct{} {
	cancelled := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				status, err := d.client.GetTaskStatus(ctx, taskID)
				if !shouldInterruptAgent(status, err) {
					continue
				}
				if err != nil {
					taskLog.Info("task gone server-side, interrupting agent", "error", err)
				} else {
					taskLog.Info("task reached terminal state server-side, interrupting agent", "status", status)
				}
				close(cancelled)
				return
			}
		}
	}()
	return cancelled
}

