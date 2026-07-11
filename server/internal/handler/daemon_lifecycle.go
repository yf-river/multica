package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/daemonws"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) DaemonRegister(w http.ResponseWriter, r *http.Request) {
	var req DaemonRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.DaemonID = strings.TrimSpace(req.DaemonID)
	req.DeviceName = strings.TrimSpace(req.DeviceName)

	if req.DaemonID == "" {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return
	}
	if req.WorkspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	if len(req.Runtimes) == 0 {
		writeError(w, http.StatusBadRequest, "at least one runtime is required")
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, req.WorkspaceID, "workspace_id")
	if !ok {
		return
	}
	req.WorkspaceID = uuidToString(wsUUID)

	// Verify workspace access and resolve owner.
	// Daemon tokens (mdt_) prove workspace access directly; OwnerID will be zero
	// (the SQL COALESCE preserves any existing owner on upsert).
	// PAT/JWT tokens require a membership check and set OwnerID from the member.
	var ownerID pgtype.UUID
	if daemonWsID := middleware.DaemonWorkspaceIDFromContext(r.Context()); daemonWsID != "" {
		if daemonWsID != req.WorkspaceID {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		// ownerID stays zero — COALESCE keeps the existing owner on upsert.
	} else {
		member, ok := h.requireWorkspaceMember(w, r, req.WorkspaceID, "workspace not found")
		if !ok {
			return
		}
		ownerID = member.UserID
	}

	ws, err := h.Queries.GetWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}

	resp := make([]AgentRuntimeResponse, 0, len(req.Runtimes))
	for _, runtime := range req.Runtimes {
		provider := normalizeProvider(runtime.Type)
		if provider == "" {
			provider = "unknown"
		}
		name := strings.TrimSpace(runtime.Name)
		if name == "" {
			name = provider
			if req.DeviceName != "" {
				name = fmt.Sprintf("%s (%s)", provider, req.DeviceName)
			}
		}
		deviceInfo := strings.TrimSpace(req.DeviceName)
		if runtime.Version != "" && deviceInfo != "" {
			deviceInfo = fmt.Sprintf("%s · %s", deviceInfo, runtime.Version)
		} else if runtime.Version != "" {
			deviceInfo = runtime.Version
		}
		status := "online"
		if runtime.Status == "offline" {
			status = "offline"
		}
		metadataMap := map[string]any{
			"version":     runtime.Version,
			"cli_version": req.CLIVersion,
			"launched_by": req.LaunchedBy,
		}
		if len(runtime.Metadata) > 0 {
			var incoming map[string]any
			if err := json.Unmarshal(runtime.Metadata, &incoming); err != nil {
				writeError(w, http.StatusBadRequest, "invalid runtime metadata")
				return
			}
			for k, v := range incoming {
				metadataMap[k] = v
			}
		}
		metadata, _ := json.Marshal(metadataMap)

		var registered db.AgentRuntime
		var inserted bool
		isCustom := strings.TrimSpace(runtime.ProfileID) != ""

		if isCustom {
			profileUUID, pok := parseUUIDOrBadRequest(w, strings.TrimSpace(runtime.ProfileID), "profile_id")
			if !pok {
				return
			}
			// The profile must exist in this workspace and be enabled. Trust
			// the profile's stored protocol_family over the daemon-sent type so
			// the provider used for task routing cannot drift from the profile.
			profile, perr := h.Queries.GetRuntimeProfileForWorkspace(r.Context(), db.GetRuntimeProfileForWorkspaceParams{
				ID:          profileUUID,
				WorkspaceID: wsUUID,
			})
			if perr != nil {
				writeError(w, http.StatusBadRequest, "unknown runtime profile: "+runtime.ProfileID)
				return
			}
			if !profile.Enabled {
				writeError(w, http.StatusConflict, "runtime profile is disabled: "+runtime.ProfileID)
				return
			}
			provider = profile.ProtocolFamily

			prow, err := h.Queries.UpsertAgentRuntimeWithProfile(r.Context(), db.UpsertAgentRuntimeWithProfileParams{
				WorkspaceID: wsUUID,
				DaemonID:    strToText(req.DaemonID),
				Name:        name,
				RuntimeMode: "local",
				Provider:    provider,
				Status:      status,
				DeviceInfo:  deviceInfo,
				Metadata:    metadata,
				OwnerID:     ownerID,
				ProfileID:   profileUUID,
			})
			if err != nil {
				obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.RuntimeFailed(
					uuidToString(ownerID),
					req.WorkspaceID,
					req.DaemonID,
					provider,
					"registration_failed",
					"db_error",
					true,
				))
				writeError(w, http.StatusInternalServerError, "failed to register runtime: "+err.Error())
				return
			}
			inserted = prow.Inserted
			registered = db.AgentRuntime{
				ID:          prow.ID,
				WorkspaceID: prow.WorkspaceID,
				DaemonID:    prow.DaemonID,
				Name:        prow.Name,
				RuntimeMode: prow.RuntimeMode,
				Provider:    prow.Provider,
				Status:      prow.Status,
				DeviceInfo:  prow.DeviceInfo,
				Metadata:    prow.Metadata,
				LastSeenAt:  prow.LastSeenAt,
				CreatedAt:   prow.CreatedAt,
				UpdatedAt:   prow.UpdatedAt,
				OwnerID:     prow.OwnerID,
				Scope:       prow.Scope,
				ProfileID:   prow.ProfileID,
			}
		} else {
			row, err := h.Queries.UpsertAgentRuntime(r.Context(), db.UpsertAgentRuntimeParams{
				WorkspaceID: wsUUID,
				DaemonID:    strToText(req.DaemonID),
				Name:        name,
				RuntimeMode: "local",
				Provider:    provider,
				Status:      status,
				DeviceInfo:  deviceInfo,
				Metadata:    metadata,
				OwnerID:     ownerID,
			})
			if err != nil {
				obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.RuntimeFailed(
					uuidToString(ownerID),
					req.WorkspaceID,
					req.DaemonID,
					provider,
					"registration_failed",
					"db_error",
					true,
				))
				writeError(w, http.StatusInternalServerError, "failed to register runtime: "+err.Error())
				return
			}
			inserted = row.Inserted
			registered = db.AgentRuntime{
				ID:          row.ID,
				WorkspaceID: row.WorkspaceID,
				DaemonID:    row.DaemonID,
				Name:        row.Name,
				RuntimeMode: row.RuntimeMode,
				Provider:    row.Provider,
				Status:      row.Status,
				DeviceInfo:  row.DeviceInfo,
				Metadata:    row.Metadata,
				LastSeenAt:  row.LastSeenAt,
				CreatedAt:   row.CreatedAt,
				UpdatedAt:   row.UpdatedAt,
				OwnerID:     row.OwnerID,
				Scope:       row.Scope,
				ProfileID:   row.ProfileID,
			}
		}

		// Inserted is false for normal daemon reconnects/upserts, so
		// runtime_ready is a first-ready-per-runtime-row signal.
		if inserted {
			obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.RuntimeRegistered(
				uuidToString(ownerID),
				req.WorkspaceID,
				uuidToString(registered.ID),
				req.DaemonID,
				provider,
				runtime.Version,
				req.CLIVersion,
			))
			if registered.Status == "online" {
				obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.RuntimeReady(
					uuidToString(ownerID),
					req.WorkspaceID,
					uuidToString(registered.ID),
					req.DaemonID,
					provider,
					0,
				))
			}
		}

		resp = append(resp, runtimeToResponse(registered))
	}

	slog.Info("daemon registered", "workspace_id", req.WorkspaceID, "daemon_id", req.DaemonID, "runtimes_count", len(resp))

	h.publish(protocol.EventDaemonRegister, req.WorkspaceID, "system", "", map[string]any{
		"runtimes": resp,
	})

	repoResp := workspaceReposResponse(req.WorkspaceID, ws.Repos, ws.Settings)

	writeJSON(w, http.StatusOK, map[string]any{
		"runtimes":      resp,
		"repos":         repoResp.Repos,
		"repos_version": repoResp.ReposVersion,
		"settings":      repoResp.Settings,
	})
}

func (h *Handler) GetDaemonWorkspaceRepos(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "workspaceId"))
	if !h.requireDaemonWorkspaceAccess(w, r, workspaceID) {
		return
	}

	ws, err := h.Queries.GetWorkspace(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}

	writeJSON(w, http.StatusOK, workspaceReposResponse(workspaceID, ws.Repos, ws.Settings))
}

// DaemonDeregister marks runtimes as offline when the daemon shuts down.
func (h *Handler) DaemonDeregister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuntimeIDs []string `json:"runtime_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.RuntimeIDs) == 0 {
		writeError(w, http.StatusBadRequest, "runtime_ids is required")
		return
	}
	runtimeUUIDs, ok := parseUUIDSliceOrBadRequest(w, req.RuntimeIDs, "runtime_ids")
	if !ok {
		return
	}

	// Track affected workspaces for WS notifications.
	affectedWorkspaces := make(map[string]bool)

	for i, rid := range req.RuntimeIDs {
		// Look up the runtime and verify ownership.
		rt, err := h.Queries.GetAgentRuntime(r.Context(), runtimeUUIDs[i])
		if err != nil {
			slog.Warn("deregister: runtime not found", "runtime_id", rid, "error", err)
			continue
		}

		wsID := uuidToString(rt.WorkspaceID)
		if !h.verifyDaemonWorkspaceAccess(r, wsID) {
			slog.Warn("deregister: workspace mismatch", "runtime_id", rid)
			continue
		}

		if err := h.Queries.SetAgentRuntimeOffline(r.Context(), rt.ID); err != nil {
			slog.Warn("deregister: failed to set offline", "runtime_id", rid, "error", err)
			continue
		}
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.RuntimeOffline(
			uuidToString(rt.OwnerID),
			wsID,
			uuidToString(rt.ID),
			rt.DaemonID.String,
			rt.Provider,
		))

		affectedWorkspaces[wsID] = true
	}

	// Notify frontend clients so they re-fetch runtime list.
	for wsID := range affectedWorkspaces {
		h.publish(protocol.EventDaemonRegister, wsID, "system", "", map[string]any{
			"action": "deregister",
		})
	}

	slog.Info("daemon deregistered", "runtime_ids", req.RuntimeIDs)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type DaemonHeartbeatRequest struct {
	RuntimeID           string          `json:"runtime_id"`
	SupportsBatchImport bool            `json:"supports_batch_import,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
}

// heartbeatHasPendingTimeout bounds the cheap HasPending probe on the
// heartbeat hot path. Probes are read-only (ZCARD in Redis) so a timeout is
// ack-safe: the worst case is "we didn't find out if anything was queued this
// tick" and the next heartbeat (default 15s later) will try again.
//
// PopPending is deliberately NOT bounded this way — its Redis implementation
// runs a Lua claim script whose ZREM + SET-running side effects cannot be
// cleanly un-run from the client side if the context expires mid-script. We
// therefore only invoke PopPending after HasPending confirms there is work
// to claim, so we never start a claim we might have to abort.
const heartbeatHasPendingTimeout = 1 * time.Second

// maxLocalSkillImportBatch is how many pending import requests the heartbeat
// handler pops per cycle. Higher values let the daemon process more imports
// in parallel but increase per-heartbeat latency.
//
// Timeout invariant: IMPORT_CONCURRENCY (views/.../runtime-local-skill-import-panel.tsx)
// × heartbeat period (~15s) must stay within runtimeLocalSkillPendingTimeout
// (runtime_local_skills.go), and IMPORT_POLL_TIMEOUT_MS (core/runtimes/local-skills.ts)
// must exceed pendingTimeout + runningTimeout.
const maxLocalSkillImportBatch = 10

// runtimeLivenessTTL is how long a Redis liveness record stays valid before
// expiring. The daemon refreshes it every heartbeat (~15s), so this just
// needs to be a few heartbeats long — the value (90s) tolerates ~6 missed
// beats before Redis declares the runtime dead.
//
// It is intentionally shorter than the sweeper's stale threshold (150s in
// cmd/server/runtime_sweeper.go). That ordering is safe and desirable:
// Redis can declare a runtime dead before the DB stale window opens, and
// the sweeper will simply ignore it until the DB column also crosses the
// threshold. The unsafe direction would be the opposite (Redis claiming
// "alive" past the DB stale window, masking a truly dead runtime when the
// sweeper consults Redis as the source of truth) — that cannot happen here.
const runtimeLivenessTTL = 90 * time.Second

// runtimeHeartbeatDBFlushInterval is the maximum staleness we tolerate on
// agent_runtime.last_seen_at while Redis is the active liveness source. When
// last_seen_at gets older than this, the heartbeat path schedules a DB write
// so (a) the UI's "last seen" display stays bounded and (b) the sweeper's
// DB-only fallback path (used when an IsAliveBatch call to Redis errors) does
// not false-positive on alive-but-Redis-only runtimes.
//
// Load-bearing invariant: this must be strictly less than the sweeper's
// stale threshold (150s in cmd/server/runtime_sweeper.go) MINUS one daemon
// heartbeat cycle (~15s) MINUS the BatchedHeartbeatScheduler tick interval
// (~30s). Worst-case DB age for an alive runtime is therefore bounded by
// flush + heartbeat + batchTick = 60 + 15 + 30 = 105s, leaving a 45s buffer
// below the 150s stale window. If you tune any of these constants, recompute
// the chain and keep at least a one-tick buffer.
//
// We intentionally keep the per-runtime flush throttle at 60s (rather than
// pushing it higher) so a crashed runtime is detected within ~150s instead
// of ~10 minutes. The bulk of the DB-pressure win comes from batched
// coalescing in HeartbeatScheduler — at 70 online runtimes that collapses
// ~17 single-row UPDATE/s into ~0.03 bulk UPDATE/s (one per batch tick),
// independent of how the per-runtime throttle is tuned.
const runtimeHeartbeatDBFlushInterval = 60 * time.Second

func (h *Handler) DaemonHeartbeat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	authPath := middleware.DaemonAuthPathFromContext(r.Context())
	var (
		outcome                                                                                            = "unauth"
		runtimeID                                                                                          string
		decodeMs, runtimeLookupMs, workspaceCheckMs                                                        int64
		authMs, updateMs, probeModelMs, popModelMs, probeSkillsMs, popSkillsMs, probeImportMs, popImportMs int64
		probeModelTimedOut, probeSkillsTimedOut, probeImportTimedOut                                       bool
	)
	defer func() {
		logHeartbeatEndpointSlow(runtimeID, outcome, authPath, start, decodeMs, runtimeLookupMs, workspaceCheckMs, authMs, updateMs, probeModelMs, popModelMs, probeSkillsMs, popSkillsMs, probeImportMs, popImportMs, probeModelTimedOut, probeSkillsTimedOut, probeImportTimedOut)
	}()

	decodeStart := time.Now()
	var req DaemonHeartbeatRequest
	decodeErr := json.NewDecoder(r.Body).Decode(&req)
	decodeMs = time.Since(decodeStart).Milliseconds()
	if decodeErr != nil {
		outcome = "bad_body"
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.RuntimeID == "" {
		outcome = "missing_runtime_id"
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}
	runtimeID = req.RuntimeID

	// Inlined and instrumented version of requireDaemonRuntimeAccess so we
	// can attribute the runtime-lookup and workspace-check sub-stages
	// independently in slow-logs. Together with the auth_path label set by
	// DaemonAuth middleware, this lets us tell whether prod heartbeat tail
	// latency is in pgx pool acquisition (runtime_lookup_ms), in the PAT
	// fallback workspace-membership query (workspace_check_ms), or upstream.
	runtimeUUID, ok := parseUUIDOrBadRequest(w, req.RuntimeID, "runtime_id")
	if !ok {
		outcome = "bad_runtime_id"
		return
	}
	lookupStart := time.Now()
	rt, lookupErr := h.Queries.GetAgentRuntime(r.Context(), runtimeUUID)
	runtimeLookupMs = time.Since(lookupStart).Milliseconds()
	if lookupErr != nil {
		// Only pgx.ErrNoRows means the runtime row is gone. Daemon reads this
		// 404 as a signal to drop the stale runtime locally; treating a
		// transient DB error the same way would force daemons to self-cleanup
		// on a hiccup.
		if isNotFound(lookupErr) {
			outcome = "runtime_not_found"
			writeError(w, http.StatusNotFound, "runtime not found")
			return
		}
		outcome = "runtime_lookup_error"
		slog.Warn("get agent runtime failed", "runtime_id", req.RuntimeID, "error", lookupErr)
		writeError(w, http.StatusInternalServerError, "failed to load runtime")
		return
	}
	wsCheckStart := time.Now()
	wsOK := h.requireDaemonWorkspaceAccess(w, r, uuidToString(rt.WorkspaceID))
	workspaceCheckMs = time.Since(wsCheckStart).Milliseconds()
	if !wsOK {
		outcome = "workspace_denied"
		return
	}
	authMs = time.Since(start).Milliseconds()

	if len(req.Metadata) > 0 {
		var metadata map[string]any
		if err := json.Unmarshal(req.Metadata, &metadata); err != nil || metadata == nil {
			outcome = "bad_metadata"
			writeError(w, http.StatusBadRequest, "invalid heartbeat metadata")
			return
		}
		if err := h.Queries.MergeAgentRuntimeMetadata(r.Context(), db.MergeAgentRuntimeMetadataParams{
			ID:       rt.ID,
			Metadata: req.Metadata,
		}); err != nil {
			outcome = "metadata_update_error"
			slog.Warn("merge runtime metadata failed", "runtime_id", req.RuntimeID, "error", err)
			writeError(w, http.StatusInternalServerError, "heartbeat failed")
			return
		}
	}

	ack, m, err := h.processHeartbeat(r.Context(), rt, req.SupportsBatchImport)
	updateMs = m.UpdateMs
	probeModelMs = m.ProbeModelMs
	popModelMs = m.PopModelMs
	probeSkillsMs = m.ProbeSkillsMs
	popSkillsMs = m.PopSkillsMs
	probeImportMs = m.ProbeImportMs
	popImportMs = m.PopImportMs
	probeModelTimedOut = m.ProbeModelTimedOut
	probeSkillsTimedOut = m.ProbeSkillsTimedOut
	probeImportTimedOut = m.ProbeImportTimedOut
	if err != nil {
		outcome = "error_update"
		writeError(w, http.StatusInternalServerError, "heartbeat failed")
		return
	}

	outcome = "ok"
	// Preserve the existing HTTP response shape: the runtime_id field is new
	// in the WS path and would be redundant noise on the HTTP path where the
	// caller already knows which runtime it asked about.
	resp := map[string]any{"status": ack.Status}
	if ack.PendingModelList != nil {
		resp["pending_model_list"] = ack.PendingModelList
	}
	if ack.PendingLocalSkills != nil {
		resp["pending_local_skills"] = ack.PendingLocalSkills
	}
	if ack.PendingLocalSkillImport != nil {
		resp["pending_local_skill_import"] = ack.PendingLocalSkillImport
	}
	if len(ack.PendingLocalSkillImports) > 0 {
		resp["pending_local_skill_imports"] = ack.PendingLocalSkillImports
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleDaemonWSHeartbeat is the daemonws.HeartbeatHandler entry point: it
// resolves the runtime, verifies the connection's workspace owns it, and
// returns the ack payload. It is the WebSocket-side mirror of DaemonHeartbeat.
//
// Workspace authorization is re-checked on every heartbeat instead of trusted
// from the upgrade-time check because runtime ownership can change (e.g. a
// runtime is reassigned to another workspace mid-connection).
//
// When the runtime row is missing (pgx.ErrNoRows), the function returns a
// successful ack with Status=HeartbeatStatusRuntimeGone and RuntimeGone=true
// instead of an error. That keeps the hub from logging every beat at Warn,
// and tells the daemon to drop the stale runtime and re-register. Other DB
// errors still propagate as errors so they keep their existing Warn logging
// and the daemon does not mistake a hiccup for a deletion.
func (h *Handler) HandleDaemonWSHeartbeat(ctx context.Context, identity daemonws.ClientIdentity, runtimeID string, supportsBatchImport bool) (*protocol.DaemonHeartbeatAckPayload, error) {
	runtimeUUID, err := util.ParseUUID(runtimeID)
	if err != nil {
		return nil, fmt.Errorf("invalid runtime_id: %w", err)
	}
	rt, err := h.Queries.GetAgentRuntime(ctx, runtimeUUID)
	if err != nil {
		if isNotFound(err) {
			return &protocol.DaemonHeartbeatAckPayload{
				RuntimeID:   runtimeID,
				Status:      protocol.HeartbeatStatusRuntimeGone,
				RuntimeGone: true,
			}, nil
		}
		return nil, fmt.Errorf("get agent runtime: %w", err)
	}
	if !identity.AllowsWorkspace(uuidToString(rt.WorkspaceID)) {
		return nil, fmt.Errorf("runtime not in connection workspace")
	}
	ack, _, err := h.processHeartbeat(ctx, rt, supportsBatchImport)
	return ack, err
}

// recordHeartbeat marks the runtime as alive. When LivenessStore is available
// (Redis configured and reachable) it writes a TTL'd liveness key and skips
// the DB row write on most beats — the DB is only updated on the
// offline→online transition or once per runtimeHeartbeatDBFlushInterval to
// keep last_seen_at fresh enough for the UI and the DB-fallback sweeper.
//
// When LivenessStore is unavailable (no Redis configured) or any Touch call
// errors, recordHeartbeat falls back to writing the DB on every beat — that
// is the original behavior and keeps the sweeper's DB-only path correct.
//
// The actual DB write is delegated to h.HeartbeatScheduler so production can
// coalesce many runtimes' bumps into one bulk UPDATE per tick. See
// heartbeat_scheduler.go for the two implementations.
func (h *Handler) recordHeartbeat(ctx context.Context, rt db.AgentRuntime) error {
	now := time.Now()

	// Decide whether the DB row needs a write *before* touching Redis, so a
	// Touch failure can simply force needDBWrite=true without re-evaluating
	// the structural reasons.
	needDBWrite := !h.LivenessStore.Available() ||
		rt.Status != "online" ||
		!rt.LastSeenAt.Valid ||
		now.Sub(rt.LastSeenAt.Time) >= runtimeHeartbeatDBFlushInterval

	if h.LivenessStore.Available() {
		if err := h.LivenessStore.Touch(ctx, uuidToString(rt.ID), runtimeLivenessTTL); err != nil {
			// Redis hiccup: degrade transparently to the DB-only path for
			// this beat. The sweeper falls back to its DB threshold the
			// same way when IsAliveBatch fails, so end-to-end correctness
			// is preserved.
			slog.Warn("liveness touch failed; falling back to DB heartbeat",
				"runtime_id", uuidToString(rt.ID), "error", err)
			needDBWrite = true
		}
	}

	if !needDBWrite {
		return nil
	}

	// Either bumps last_seen_at on an already-online row (Touch + race
	// fallback) or flips status from offline to online. The scheduler
	// chooses sync vs batched per case; see HeartbeatScheduler doc.
	return h.HeartbeatScheduler.Schedule(ctx, rt)
}

// heartbeatMetrics carries per-stage timings out of processHeartbeat so the
// HTTP slow-log can stay structured. The WS path discards them.
type heartbeatMetrics struct {
	UpdateMs, ProbeModelMs, PopModelMs, ProbeSkillsMs, PopSkillsMs, ProbeImportMs, PopImportMs int64
	ProbeModelTimedOut, ProbeSkillsTimedOut, ProbeImportTimedOut                               bool
}

// processHeartbeat does the work shared by HTTP POST /api/daemon/heartbeat and
// the WebSocket daemon:heartbeat path: records liveness and pulls any pending
// actions queued for the runtime. Auth and request decoding live in the
// caller because they differ between transports.
func (h *Handler) processHeartbeat(ctx context.Context, rt db.AgentRuntime, supportsBatchImport bool) (*protocol.DaemonHeartbeatAckPayload, heartbeatMetrics, error) {
	var m heartbeatMetrics
	runtimeID := uuidToString(rt.ID)

	updateStart := time.Now()
	if err := h.recordHeartbeat(ctx, rt); err != nil {
		m.UpdateMs = time.Since(updateStart).Milliseconds()
		return nil, m, err
	}
	m.UpdateMs = time.Since(updateStart).Milliseconds()

	slog.Debug("daemon heartbeat", "runtime_id", runtimeID)

	ack := &protocol.DaemonHeartbeatAckPayload{
		RuntimeID: runtimeID,
		Status:    "ok",
	}

	// Probe then claim the model list queue. Same pattern as the local-skill
	// queues below — a slow shared store cannot stall the heartbeat on
	// empty-queue ticks, but the claim itself runs unbounded because its
	// Lua side effects cannot be safely aborted mid-script.
	probeModelStart := time.Now()
	probeModelCtx, cancelProbeModel := context.WithTimeout(ctx, heartbeatHasPendingTimeout)
	hasModel, probeModelErr := h.ModelListStore.HasPending(probeModelCtx, runtimeID)
	cancelProbeModel()
	m.ProbeModelMs = time.Since(probeModelStart).Milliseconds()
	switch {
	case probeModelErr == nil && hasModel:
		popStart := time.Now()
		pendingModel, popErr := h.ModelListStore.PopPending(ctx, runtimeID)
		m.PopModelMs = time.Since(popStart).Milliseconds()
		if popErr != nil {
			slog.Warn("model list PopPending failed", "error", popErr, "runtime_id", runtimeID)
		} else if pendingModel != nil {
			ack.PendingModelList = &protocol.DaemonHeartbeatPendingModelList{ID: pendingModel.ID}
		}
	case probeModelErr != nil:
		if errors.Is(probeModelErr, context.DeadlineExceeded) || errors.Is(probeModelErr, context.Canceled) {
			m.ProbeModelTimedOut = true
			slog.Warn("model list HasPending timed out", "runtime_id", runtimeID, "elapsed_ms", m.ProbeModelMs)
		} else {
			slog.Warn("model list HasPending failed", "error", probeModelErr, "runtime_id", runtimeID)
		}
	}

	// Probe then claim the local-skill list queue. The probe is bounded so a
	// slow shared store cannot stall the heartbeat on empty-queue ticks; the
	// claim runs unbounded (it inherits only ctx) because its Lua side
	// effects cannot be safely aborted mid-script.
	probeSkillsStart := time.Now()
	probeSkillsCtx, cancelProbeSkills := context.WithTimeout(ctx, heartbeatHasPendingTimeout)
	hasSkills, probeErr := h.LocalSkillListStore.HasPending(probeSkillsCtx, runtimeID)
	cancelProbeSkills()
	m.ProbeSkillsMs = time.Since(probeSkillsStart).Milliseconds()
	switch {
	case probeErr == nil && hasSkills:
		popStart := time.Now()
		pendingSkills, popErr := h.LocalSkillListStore.PopPending(ctx, runtimeID)
		m.PopSkillsMs = time.Since(popStart).Milliseconds()
		if popErr != nil {
			slog.Warn("local skill list PopPending failed", "error", popErr, "runtime_id", runtimeID)
		} else if pendingSkills != nil {
			ack.PendingLocalSkills = &protocol.DaemonHeartbeatPendingLocalSkills{ID: pendingSkills.ID}
		}
	case probeErr != nil:
		if errors.Is(probeErr, context.DeadlineExceeded) || errors.Is(probeErr, context.Canceled) {
			m.ProbeSkillsTimedOut = true
			slog.Warn("local skill list HasPending timed out", "runtime_id", runtimeID, "elapsed_ms", m.ProbeSkillsMs)
		} else {
			slog.Warn("local skill list HasPending failed", "error", probeErr, "runtime_id", runtimeID)
		}
	}

	probeImportStart := time.Now()
	probeImportCtx, cancelProbeImport := context.WithTimeout(ctx, heartbeatHasPendingTimeout)
	hasImport, probeErr := h.LocalSkillImportStore.HasPending(probeImportCtx, runtimeID)
	cancelProbeImport()
	m.ProbeImportMs = time.Since(probeImportStart).Milliseconds()
	switch {
	case probeErr == nil && hasImport:
		popStart := time.Now()
		if supportsBatchImport {
			pendingImports, popErr := h.LocalSkillImportStore.PopPendingBatch(ctx, runtimeID, maxLocalSkillImportBatch)
			m.PopImportMs = time.Since(popStart).Milliseconds()
			if popErr != nil {
				slog.Warn("local skill import PopPendingBatch failed", "error", popErr, "runtime_id", runtimeID, "claimed", len(pendingImports))
			}
			// Always dispatch whatever was claimed — even on partial
			// failure the claimed requests have already transitioned to
			// running in the store. Dropping them here would leave them
			// stranded until the running timeout.
			if len(pendingImports) > 0 {
				// Backwards compat: singular field carries the first item so
				// old daemons that don't know the plural field still get one.
				ack.PendingLocalSkillImport = &protocol.DaemonHeartbeatPendingLocalSkillImport{
					ID:       pendingImports[0].ID,
					SkillKey: pendingImports[0].SkillKey,
				}
				batch := make([]protocol.DaemonHeartbeatPendingLocalSkillImport, 0, len(pendingImports))
				for _, p := range pendingImports {
					batch = append(batch, protocol.DaemonHeartbeatPendingLocalSkillImport{
						ID:       p.ID,
						SkillKey: p.SkillKey,
					})
				}
				ack.PendingLocalSkillImports = batch
			}
		} else {
			pendingImport, popErr := h.LocalSkillImportStore.PopPending(ctx, runtimeID)
			m.PopImportMs = time.Since(popStart).Milliseconds()
			if popErr != nil {
				slog.Warn("local skill import PopPending failed", "error", popErr, "runtime_id", runtimeID)
			} else if pendingImport != nil {
				ack.PendingLocalSkillImport = &protocol.DaemonHeartbeatPendingLocalSkillImport{
					ID:       pendingImport.ID,
					SkillKey: pendingImport.SkillKey,
				}
			}
		}
	case probeErr != nil:
		if errors.Is(probeErr, context.DeadlineExceeded) || errors.Is(probeErr, context.Canceled) {
			m.ProbeImportTimedOut = true
			slog.Warn("local skill import HasPending timed out", "runtime_id", runtimeID, "elapsed_ms", m.ProbeImportMs)
		} else {
			slog.Warn("local skill import HasPending failed", "error", probeErr, "runtime_id", runtimeID)
		}
	}

	return ack, m, nil
}

// logHeartbeatEndpointSlow emits one structured log when /api/daemon/heartbeat
// exceeds 500ms, splitting auth / update / probe / pop phases for both queues
// so the prod tail can be attributed without flooding logs at normal rates.
// auth_ms is further decomposed into decode_ms, runtime_lookup_ms, and
// workspace_check_ms; auth_path labels which token kind authenticated the
// request ("daemon_token", "pat", or "jwt"). Mirrors logClaimEndpointSlow.
func logHeartbeatEndpointSlow(runtimeID, outcome, authPath string, start time.Time, decodeMs, runtimeLookupMs, workspaceCheckMs, authMs, updateMs, probeModelMs, popModelMs, probeSkillsMs, popSkillsMs, probeImportMs, popImportMs int64, probeModelTimedOut, probeSkillsTimedOut, probeImportTimedOut bool) {
	totalMs := time.Since(start).Milliseconds()
	if totalMs < 500 && !probeModelTimedOut && !probeSkillsTimedOut && !probeImportTimedOut {
		return
	}
	slog.Info("heartbeat_endpoint slow",
		"runtime_id", runtimeID,
		"outcome", outcome,
		"auth_path", authPath,
		"total_ms", totalMs,
		"auth_ms", authMs,
		"decode_ms", decodeMs,
		"runtime_lookup_ms", runtimeLookupMs,
		"workspace_check_ms", workspaceCheckMs,
		"update_ms", updateMs,
		"probe_model_ms", probeModelMs,
		"pop_model_ms", popModelMs,
		"probe_skills_ms", probeSkillsMs,
		"pop_skills_ms", popSkillsMs,
		"probe_import_ms", probeImportMs,
		"pop_import_ms", popImportMs,
		"probe_model_timed_out", probeModelTimedOut,
		"probe_skills_timed_out", probeSkillsTimedOut,
		"probe_import_timed_out", probeImportTimedOut,
	)
}

// logClaimEndpointSlow emits one structured log when the /tasks/claim endpoint
// exceeds 500ms, splitting auth / claim / response-build phases so the prod
// tail can be diagnosed without flooding logs at normal poll rates.
func logClaimEndpointSlow(runtimeID, outcome string, start time.Time, authMs, claimMs, buildMs int64) {
	totalMs := time.Since(start).Milliseconds()
	if totalMs < 500 {
		return
	}
	slog.Info("claim_endpoint slow",
		"runtime_id", runtimeID,
		"outcome", outcome,
		"total_ms", totalMs,
		"auth_ms", authMs,
		"claim_ms", claimMs,
		"build_ms", buildMs,
	)
}

func roleKeyFromAgentRuntimeConfig(agent db.Agent) string {
	var runtimeConfig map[string]any
	if len(bytes.TrimSpace(agent.RuntimeConfig)) == 0 || json.Unmarshal(agent.RuntimeConfig, &runtimeConfig) != nil {
		return ""
	}
	if scope, ok := runtimeConfig["internal_squad"].(map[string]any); ok {
		return stringFromAny(scope["role_key"])
	}
	return ""
}

func taskExecutionPolicyForRole(roleKey string, fallbackName string, isSquadLeader bool) TaskExecutionPolicyData {
	key := strings.ToLower(strings.TrimSpace(roleKey))
	name := strings.TrimSpace(fallbackName)
	if isSquadLeader || key == "pm" || (key == "" && (strings.EqualFold(name, "pm") || strings.HasPrefix(strings.ToUpper(name), "PM-"))) {
		return TaskExecutionPolicyData{
			RoleKey:          "pm",
			RoleKind:         "coordinator",
			CanAccessRepo:    false,
			CanEditRepo:      false,
			ProjectSkillMode: "none",
		}
	}
	switch key {
	case "01-clarify":
		return TaskExecutionPolicyData{RoleKey: "01-clarify", RoleKind: "planning_stage", CanAccessRepo: false, CanEditRepo: false, ProjectSkillMode: "none", AllowedProjectSkills: []string{"01-clarify"}}
	case "02-design":
		return TaskExecutionPolicyData{RoleKey: "02-design", RoleKind: "planning_stage", CanAccessRepo: true, CanEditRepo: false, ProjectSkillMode: "stage", AllowedProjectSkills: []string{"02-design"}}
	case "03-task-split":
		return TaskExecutionPolicyData{RoleKey: "03-task-split", RoleKind: "planning_stage", CanAccessRepo: true, CanEditRepo: false, ProjectSkillMode: "stage", AllowedProjectSkills: []string{"03-task-split"}}
	case "04-implement":
		return TaskExecutionPolicyData{RoleKey: "04-implement", RoleKind: "implementation_stage", CanAccessRepo: true, CanEditRepo: true, ProjectSkillMode: "implementation", AllowedProjectSkills: []string{"04-implement"}}
	case "05-verify":
		return TaskExecutionPolicyData{RoleKey: "05-verify", RoleKind: "verification_stage", CanAccessRepo: true, CanEditRepo: false, ProjectSkillMode: "verification", AllowedProjectSkills: []string{"05-verify"}}
	}
	switch name {
	case projectSOPAgent01:
		return TaskExecutionPolicyData{RoleKey: "01-clarify", RoleKind: "planning_stage", CanAccessRepo: false, CanEditRepo: false, ProjectSkillMode: "none", AllowedProjectSkills: []string{"01-clarify"}}
	case projectSOPAgent02:
		return TaskExecutionPolicyData{RoleKey: "02-design", RoleKind: "planning_stage", CanAccessRepo: true, CanEditRepo: false, ProjectSkillMode: "stage", AllowedProjectSkills: []string{"02-design"}}
	case projectSOPAgent03:
		return TaskExecutionPolicyData{RoleKey: "03-task-split", RoleKind: "planning_stage", CanAccessRepo: true, CanEditRepo: false, ProjectSkillMode: "stage", AllowedProjectSkills: []string{"03-task-split"}}
	case projectSOPAgent04:
		return TaskExecutionPolicyData{RoleKey: "04-implement", RoleKind: "implementation_stage", CanAccessRepo: true, CanEditRepo: true, ProjectSkillMode: "implementation", AllowedProjectSkills: []string{"04-implement"}}
	case projectSOPAgent05:
		return TaskExecutionPolicyData{RoleKey: "05-verify", RoleKind: "verification_stage", CanAccessRepo: true, CanEditRepo: false, ProjectSkillMode: "verification", AllowedProjectSkills: []string{"05-verify"}}
	default:
		return TaskExecutionPolicyData{RoleKind: "agent", CanAccessRepo: true, CanEditRepo: true, ProjectSkillMode: "all"}
	}
}

func taskExecutionPolicyForAgent(agent db.Agent, isSquadLeader bool) TaskExecutionPolicyData {
	return taskExecutionPolicyForRole(roleKeyFromAgentRuntimeConfig(agent), agent.Name, isSquadLeader)
}

func filterAgentSkillsForExecutionPolicy(skills []service.AgentSkillData, policy TaskExecutionPolicyData) []service.AgentSkillData {
	if policy.ProjectSkillMode == "" || policy.ProjectSkillMode == "all" {
		return skills
	}
	coordinatorNoRepo := isCoordinatorWithoutRepoPolicy(policy)
	allowed := make(map[string]struct{}, len(policy.AllowedProjectSkills))
	for _, name := range policy.AllowedProjectSkills {
		allowed[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	out := make([]service.AgentSkillData, 0, len(skills))
	for _, skill := range skills {
		name := strings.ToLower(strings.TrimSpace(skill.Name))
		if strings.HasPrefix(name, "multica-") {
			if coordinatorNoRepo && !coordinatorBuiltinSkillAllowed(name) {
				continue
			}
			out = append(out, skill)
			continue
		}
		if _, ok := allowed[name]; ok {
			out = append(out, skill)
		}
	}
	return out
}

func filterBuiltinSkillsForExecutionPolicy(skills []service.AgentSkillData, policy TaskExecutionPolicyData) []service.AgentSkillData {
	if !isCoordinatorWithoutRepoPolicy(policy) {
		return skills
	}
	out := make([]service.AgentSkillData, 0, len(skills))
	for _, skill := range skills {
		name := strings.ToLower(strings.TrimSpace(skill.Name))
		if coordinatorBuiltinSkillAllowed(name) {
			out = append(out, skill)
		}
	}
	return out
}

func isCoordinatorWithoutRepoPolicy(policy TaskExecutionPolicyData) bool {
	return strings.EqualFold(strings.TrimSpace(policy.RoleKind), "coordinator") && !policy.CanAccessRepo
}

func isNoRepoBoundedPolicy(policy *TaskExecutionPolicyData) bool {
	if policy == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(policy.RoleKind)) {
	case "planning_stage", "verification_stage":
		return !policy.CanAccessRepo
	default:
		return false
	}
}

func coordinatorBuiltinSkillAllowed(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "multica-mentioning", "multica-projects-and-resources", "multica-squads":
		return true
	default:
		return false
	}
}

// ClaimTaskByRuntime atomically claims the next queued task for a runtime.
// The response includes the agent's name and skills, fetched fresh from the DB.
