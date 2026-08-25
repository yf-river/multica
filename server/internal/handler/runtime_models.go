package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/pkg/agent"
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
	Models    []agent.Model `json:"models,omitempty"`
	Supported bool          `json:"supported"`
}

func (request *ModelListRequest) asyncRequestState() *runtimeAsyncRequestState {
	return &request.runtimeAsyncRequestState
}

const (
	// modelListStoreRetention bounds how long any stored request lives in
	// the backing store. The Redis backend uses it as a TTL; the in-memory
	// backend GCs on Create. The window is deliberately wider than the
	// running/pending timeouts so terminal records are still readable when
	// the UI's last poll arrives.
	modelListStoreRetention = 2 * time.Minute
)

func NewInMemoryModelListStore() *inMemoryRuntimeListStore[ModelListRequest, agent.Model] {
	return newInMemoryRuntimeListStore(
		modelListStoreRetention,
		(*ModelListRequest).asyncRequestState,
		applyRuntimeListTimeout,
		func(runtimeID, requestID string, now time.Time) *ModelListRequest {
			return &ModelListRequest{
				runtimeAsyncRequestState: runtimeAsyncRequestState{
					ID: requestID, RuntimeID: runtimeID, Status: runtimeAsyncPending,
					CreatedAt: now, UpdatedAt: now,
				},
				Supported: true,
			}
		},
		func(request *ModelListRequest, models []agent.Model, supported bool) {
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
	createRuntimeListRequest(w, r, uuidToString(rt.ID), h.ModelListStore, "failed to enqueue model list request")
}

// GetModelListRequest returns the status of a model list request.
func (h *Handler) GetModelListRequest(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}

	request, _, ok := loadRuntimeAsyncRequest(w, r, uuidToString(rt.ID), h.ModelListStore.Get, (*ModelListRequest).asyncRequestState)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, request)
}

// ReportModelListResult receives the list result from the daemon.
func (h *Handler) ReportModelListResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")

	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	// Fetch first so we can ignore stale reports for already-terminal
	// requests (e.g. the heartbeat response that triggered the daemon
	// run was a retry, and the original report already landed).
	existing, requestID, ok := loadRuntimeAsyncRequest(w, r, runtimeID, h.ModelListStore.Get, (*ModelListRequest).asyncRequestState)
	if !ok {
		return
	}
	if acknowledgeTerminalRuntimeReport(w, runtimeID, requestID, "ignoring stale model list report", &existing.runtimeAsyncRequestState) {
		return
	}

	var body struct {
		Status    string        `json:"status"` // "completed" or "failed"
		Models    []agent.Model `json:"models"`
		Supported bool          `json:"supported"`
		Error     string        `json:"error"`
	}
	if !decodeRequiredJSON(w, r, &body) {
		return
	}

	if !persistRuntimeListResult(w, r, h.ModelListStore, requestID, "models", body.Status, body.Models, body.Supported, body.Error) {
		return
	}

	slog.Debug("model list report", "runtime_id", runtimeID, "request_id", requestID, "status", body.Status, "count", len(body.Models))
	writeRuntimeAsyncOK(w)
}
