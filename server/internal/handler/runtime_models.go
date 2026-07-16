package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
// Model list request store
// ---------------------------------------------------------------------------
//
// The server cannot call the daemon directly (the daemon is behind the user's
// NAT and only polls the server). So "list models for this runtime" uses a
// pending-request pattern: a frontend POST creates a pending request, the
// daemon pops it on the next heartbeat, executes locally, and reports the
// result back.
//
// The store is the cross-cutting state for that flow. It MUST stay coherent
// across API replicas — POST, heartbeat and poll can each land on a different
// node, and they all need to see the same request lifecycle. The single-node
// in-memory implementation is fine for self-hosted dev; multi-node deploys
// (Multica Cloud) MUST use the Redis-backed implementation, otherwise the
// pending request is invisible to whichever replica receives the next call
// and the picker shows "No models available" (regression: see issue
// review on multica-ai/multica#2009).

// ModelListRequest represents a pending or completed model list request.
// Supported is false when the provider ignores per-agent model
// selection entirely (currently: hermes). The UI uses this to
// disable its dropdown rather than silently accepting a value the
// backend will drop.
//
// RunStartedAt is set when PopPending claims the request. It is
// `json:"-"` because it's a server-side bookkeeping field — the UI only
// needs Status / UpdatedAt to drive the polling loop.
type ModelListRequest struct {
	runtimeAsyncRequestState
	Models    []ModelEntry `json:"models,omitempty"`
	Supported bool         `json:"supported"`
}

// ModelEntry mirrors agent.Model for the wire. `Default` tags the
// model the runtime advertises as its preferred pick (e.g. Claude
// Code's shipped default, or hermes' currentModelId) so the UI can
// badge it — don't drop it when marshalling.
//
// `Thinking` carries the per-model reasoning-effort catalog discovered
// by the daemon for runtimes that support it (claude, codex — see
// MUL-2339). nil means "no picker for this model"; the UI hides the
// thinking_level selector. Older daemons (pre-2026-05) won't send this
// field, which is fine: the UI hides the selector and the agent runs
// with the runtime default.
type ModelEntry struct {
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	Provider string         `json:"provider,omitempty"`
	Default  bool           `json:"default,omitempty"`
	Thinking *ModelThinking `json:"thinking,omitempty"`
}

// ModelThinking is the wire shape for the per-model thinking catalog.
// Mirrors agent.ModelThinking so the daemon's report passes through
// without remapping.
type ModelThinking struct {
	SupportedLevels []ThinkingLevel `json:"supported_levels"`
	DefaultLevel    string          `json:"default_level,omitempty"`
}

// ThinkingLevel is the wire shape for a single entry in a model's
// reasoning-effort catalog. `Value` is the literal token the daemon
// passes to the CLI; `Label` is the human-readable display string;
// `Description` is optional helper copy (Codex's debug-models output
// includes one per level).
type ThinkingLevel struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

const (
	// modelListPendingTimeout bounds how long a pending request can sit in
	// the store before the UI is told "daemon didn't pick this up".
	modelListPendingTimeout = 30 * time.Second
	// modelListStoreRetention bounds how long any stored request lives in
	// the backing store. The Redis backend uses it as a TTL; the in-memory
	// backend GCs on Create. The window is deliberately wider than the
	// running/pending timeouts so terminal records are still readable when
	// the UI's last poll arrives.
	modelListStoreRetention = 2 * time.Minute
)

// applyModelListTimeout transitions a request to runtimeAsyncTimeout when it has
// been stuck in a non-terminal state past its threshold. Returns true when
// the record was modified so callers can persist the change. The pending
// threshold catches "daemon never picked this up"; the running threshold
// catches "daemon picked it up but the result report was lost" — without
// the running escape, only retention sweep ends the polling loop.
func applyModelListTimeout(req *runtimeAsyncRequestState, now time.Time) bool {
	return applyRuntimeAsyncTimeout(
		req,
		now,
		modelListPendingTimeout,
		"daemon did not respond within 30 seconds",
	)
}

func NewInMemoryModelListStore() *inMemoryRuntimeListStore[ModelListRequest, ModelEntry] {
	return newInMemoryRuntimeListStore(
		modelListStoreRetention,
		func(request *ModelListRequest) *runtimeAsyncRequestState { return &request.runtimeAsyncRequestState },
		applyModelListTimeout,
		func(runtimeID, requestID string, now time.Time) *ModelListRequest {
			return &ModelListRequest{
				runtimeAsyncRequestState: runtimeAsyncRequestState{
					ID: requestID, RuntimeID: runtimeID, Status: runtimeAsyncPending,
					CreatedAt: now, UpdatedAt: now,
				},
				Supported: true,
			}
		},
		func(request *ModelListRequest, models []ModelEntry, supported bool) {
			request.Models = models
			request.Supported = supported
		},
	)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// InitiateListModels creates a pending model list request for a runtime.
// Called by the frontend; the daemon picks it up on its next heartbeat.
func (h *Handler) InitiateListModels(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}
	if rt.Status != "online" {
		writeError(w, http.StatusServiceUnavailable, "runtime is offline")
		return
	}
	requestID, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}

	req, err := h.ModelListStore.Create(r.Context(), uuidToString(rt.ID), uuidToString(requestID))
	if err != nil {
		if errors.Is(err, errRuntimeAsyncRequestConflict) {
			writeIdempotencyConflict(w, "Idempotency-Key was already used for another runtime")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to enqueue model list request: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// GetModelListRequest returns the status of a model list request.
func (h *Handler) GetModelListRequest(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")

	req, err := h.ModelListStore.Get(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load request: "+err.Error())
		return
	}
	if req == nil || req.RuntimeID != uuidToString(rt.ID) {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// ReportModelListResult receives the list result from the daemon.
func (h *Handler) ReportModelListResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")

	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")

	// Fetch first so we can ignore stale reports for already-terminal
	// requests (e.g. the heartbeat response that triggered the daemon
	// run was a retry, and the original report already landed).
	existing, err := h.ModelListStore.Get(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load request: "+err.Error())
		return
	}
	if existing == nil || existing.RuntimeID != runtimeID {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	if runtimeAsyncRequestTerminal(existing.Status) {
		slog.Debug("ignoring stale model list report", "runtime_id", runtimeID, "request_id", requestID, "status", existing.Status)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	var body struct {
		Status    string       `json:"status"` // "completed" or "failed"
		Models    []ModelEntry `json:"models"`
		Supported bool         `json:"supported"`
		Error     string       `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Status == "completed" {
		if err := h.ModelListStore.Complete(r.Context(), requestID, body.Models, body.Supported); err != nil {
			// Surface the store failure as 5xx so the daemon can retry instead
			// of swallowing the report (leaves the request stuck in running
			// until the server-side timeout, which is exactly the "looks OK
			// but nothing happens" class of bug we're trying to avoid).
			slog.Error("ModelListStore Complete failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist completion")
			return
		}
	} else {
		if err := h.ModelListStore.Fail(r.Context(), requestID, body.Error); err != nil {
			slog.Error("ModelListStore Fail failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist failure")
			return
		}
	}

	slog.Debug("model list report", "runtime_id", runtimeID, "request_id", requestID, "status", body.Status, "count", len(body.Models))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
