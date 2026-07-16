package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// LocalSkillImportAction selects how a runtime-local-skill import resolves when
// a skill with the same name already exists in the workspace.
type LocalSkillImportAction string

const (
	// LocalSkillImportActionCreate is the default: create a new skill, and
	// surface a structured `conflict` if the name is already taken.
	LocalSkillImportActionCreate LocalSkillImportAction = ""
	// LocalSkillImportActionOverwrite re-imports onto an existing skill,
	// identified by TargetSkillID. Only the skill's creator may overwrite.
	LocalSkillImportActionOverwrite LocalSkillImportAction = "overwrite"
)

// LocalSkillImportConflict is the structured result attached to a request that
// terminated in the shared conflict status. CanOverwrite reflects the
// creator-only re-import policy (canOverwriteSkillByLocalImport).
type LocalSkillImportConflict struct {
	ExistingSkillID   string `json:"existing_skill_id"`
	ExistingCreatedBy string `json:"existing_created_by,omitempty"`
	CanOverwrite      bool   `json:"can_overwrite"`
}

const (
	// The current daemon claims the UI's full ten-request concurrency window
	// in one heartbeat. Two heartbeat intervals distinguish delay from absence.
	runtimeLocalSkillPendingTimeout = 30 * time.Second
	runtimeLocalSkillStoreRetention = 5 * time.Minute
)

// LocalSkillImportRequestInput carries the fields needed to enqueue a
// runtime-local-skill import.
type LocalSkillImportRequestInput struct {
	RequestID     string
	RequestHash   string
	RuntimeID     string
	CreatorID     string
	SkillKey      string
	Name          *string
	Description   *string
	Action        LocalSkillImportAction
	TargetSkillID string
}

// LocalSkillImportStore owns runtime-local-skill import requests. It stays
// separate from the shared list lifecycle because the
// Create signature carries import-specific fields (skill_key, optional rename).
type LocalSkillImportStore interface {
	Create(ctx context.Context, input LocalSkillImportRequestInput) (*RuntimeLocalSkillImportRequest, error)
	Get(ctx context.Context, id string) (*RuntimeLocalSkillImportRequest, error)
	HasPending(ctx context.Context, runtimeID string) (bool, error)
	// PopPendingBatch claims up to limit pending requests atomically and
	// transitions them to running. Used by the heartbeat handler to deliver
	// multiple imports per heartbeat cycle.
	PopPendingBatch(ctx context.Context, runtimeID string, limit int) ([]*RuntimeLocalSkillImportRequest, error)
	Complete(ctx context.Context, id string, skill SkillResponse) error
	// Conflict transitions a request to the terminal conflict status,
	// state, attaching structured conflict metadata for the caller to act on.
	Conflict(ctx context.Context, id string, info LocalSkillImportConflict) error
	Fail(ctx context.Context, id string, errMsg string) error
}

func applyLocalSkillTimeout(req *runtimeAsyncRequestState, now time.Time) bool {
	return applyRuntimeAsyncTimeout(req, now, runtimeLocalSkillPendingTimeout, "daemon did not respond within 30 seconds")
}

type RuntimeLocalSkillSummary struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SourcePath  string `json:"source_path"`
	Provider    string `json:"provider"`
	FileCount   int    `json:"file_count"`
}

type RuntimeLocalSkillListRequest struct {
	runtimeAsyncRequestState
	Skills    []RuntimeLocalSkillSummary `json:"skills,omitempty"`
	Supported bool                       `json:"supported"`
}

type RuntimeLocalSkillImportRequest struct {
	runtimeAsyncRequestState
	SkillKey      string                    `json:"skill_key"`
	Name          *string                   `json:"name,omitempty"`
	Description   *string                   `json:"description,omitempty"`
	Action        LocalSkillImportAction    `json:"action,omitempty"`
	TargetSkillID string                    `json:"target_skill_id,omitempty"`
	Skill         *SkillResponse            `json:"skill,omitempty"`
	Conflict      *LocalSkillImportConflict `json:"conflict,omitempty"`
	CreatorID     string                    `json:"-"`
	RequestHash   string                    `json:"-"`
}

var errLocalSkillImportRequestConflict = errors.New("local skill import request conflict")

func NewInMemoryLocalSkillListStore() *inMemoryRuntimeListStore[RuntimeLocalSkillListRequest, RuntimeLocalSkillSummary] {
	return newInMemoryRuntimeListStore(
		runtimeLocalSkillStoreRetention,
		func(request *RuntimeLocalSkillListRequest) *runtimeAsyncRequestState {
			return &request.runtimeAsyncRequestState
		},
		applyLocalSkillTimeout,
		func(runtimeID, requestID string, now time.Time) *RuntimeLocalSkillListRequest {
			return &RuntimeLocalSkillListRequest{
				runtimeAsyncRequestState: runtimeAsyncRequestState{
					ID: requestID, RuntimeID: runtimeID, Status: runtimeAsyncPending,
					CreatedAt: now, UpdatedAt: now,
				},
				Supported: true,
			}
		},
		func(request *RuntimeLocalSkillListRequest, skills []RuntimeLocalSkillSummary, supported bool) {
			request.Skills = skills
			request.Supported = supported
		},
	)
}

// inMemoryLocalSkillImportStore owns the import-specific lifecycle. It has the
// same single-node vs. multi-node caveat as the shared list store.
type inMemoryLocalSkillImportStore struct {
	*inMemoryRuntimeAsyncStore[RuntimeLocalSkillImportRequest]
}

func NewInMemoryLocalSkillImportStore() *inMemoryLocalSkillImportStore {
	return &inMemoryLocalSkillImportStore{newInMemoryRuntimeAsyncStore(
		runtimeLocalSkillStoreRetention,
		func(request *RuntimeLocalSkillImportRequest) *runtimeAsyncRequestState {
			return &request.runtimeAsyncRequestState
		},
		applyLocalSkillTimeout,
	)}
}

func (s *inMemoryLocalSkillImportStore) Create(_ context.Context, input LocalSkillImportRequestInput) (*RuntimeLocalSkillImportRequest, error) {
	return s.create(input.RequestID, func(now time.Time) *RuntimeLocalSkillImportRequest {
		return &RuntimeLocalSkillImportRequest{
			runtimeAsyncRequestState: runtimeAsyncRequestState{
				ID: input.RequestID, RuntimeID: input.RuntimeID, Status: runtimeAsyncPending,
				CreatedAt: now, UpdatedAt: now,
			},
			SkillKey:      input.SkillKey,
			Name:          input.Name,
			Description:   input.Description,
			Action:        input.Action,
			TargetSkillID: input.TargetSkillID,
			CreatorID:     input.CreatorID,
			RequestHash:   input.RequestHash,
		}
	}, func(existing *RuntimeLocalSkillImportRequest) error {
		if existing.RequestHash != input.RequestHash {
			return errLocalSkillImportRequestConflict
		}
		return nil
	})
}

func (s *inMemoryLocalSkillImportStore) Get(_ context.Context, id string) (*RuntimeLocalSkillImportRequest, error) {
	return s.get(id), nil
}

func (s *inMemoryLocalSkillImportStore) HasPending(_ context.Context, runtimeID string) (bool, error) {
	return s.hasPending(runtimeID), nil
}

func (s *inMemoryLocalSkillImportStore) PopPendingBatch(_ context.Context, runtimeID string, limit int) ([]*RuntimeLocalSkillImportRequest, error) {
	return s.popPending(runtimeID, limit), nil
}

func (s *inMemoryLocalSkillImportStore) Complete(_ context.Context, id string, skill SkillResponse) error {
	s.update(id, func(request *RuntimeLocalSkillImportRequest, state *runtimeAsyncRequestState, now time.Time) {
		state.Status = runtimeAsyncCompleted
		request.Skill = &skill
		state.UpdatedAt = now
	})
	return nil
}

func (s *inMemoryLocalSkillImportStore) Conflict(_ context.Context, id string, info LocalSkillImportConflict) error {
	s.update(id, func(request *RuntimeLocalSkillImportRequest, state *runtimeAsyncRequestState, now time.Time) {
		state.Status = runtimeAsyncConflict
		conflict := info
		request.Conflict = &conflict
		state.UpdatedAt = now
	})
	return nil
}

func (s *inMemoryLocalSkillImportStore) Fail(_ context.Context, id string, errMsg string) error {
	s.fail(id, errMsg)
	return nil
}

type CreateRuntimeLocalSkillImportRequest struct {
	SkillKey    string  `json:"skill_key"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	// Action selects create (default) vs overwrite. When overwrite,
	// TargetSkillID must reference the existing same-name skill.
	Action        LocalSkillImportAction `json:"action,omitempty"`
	TargetSkillID string                 `json:"target_skill_id,omitempty"`
}

type reportedRuntimeLocalSkill struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Content     string                   `json:"content"`
	SourcePath  string                   `json:"source_path"`
	Provider    string                   `json:"provider"`
	Files       []CreateSkillFileRequest `json:"files,omitempty"`
}

func cleanOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func (h *Handler) requireRuntimeLocalSkillAccess(w http.ResponseWriter, r *http.Request, runtimeID string) (db.AgentRuntime, bool) {
	rt, member, ok := h.requireRuntimeAccess(w, r, runtimeID)
	if !ok {
		return db.AgentRuntime{}, false
	}

	if rt.OwnerID.Valid && uuidToString(rt.OwnerID) == uuidToString(member.UserID) {
		return rt, true
	}

	writeError(w, http.StatusForbidden, "insufficient permissions")
	return db.AgentRuntime{}, false
}

func (h *Handler) InitiateListLocalSkills(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireRuntimeLocalSkillAccess(w, r, runtimeID)
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

	req, err := h.LocalSkillListStore.Create(r.Context(), uuidToString(rt.ID), uuidToString(requestID))
	if err != nil {
		if errors.Is(err, errRuntimeAsyncRequestConflict) {
			writeIdempotencyConflict(w, "Idempotency-Key was already used for another runtime")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to enqueue local skills request: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (h *Handler) GetLocalSkillListRequest(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireRuntimeLocalSkillAccess(w, r, runtimeID)
	if !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")
	req, err := h.LocalSkillListStore.Get(r.Context(), requestID)
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

func (h *Handler) InitiateImportLocalSkill(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireRuntimeLocalSkillAccess(w, r, runtimeID)
	if !ok {
		return
	}
	if rt.Status != "online" {
		writeError(w, http.StatusServiceUnavailable, "runtime is offline")
		return
	}

	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	requestID, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}

	var req CreateRuntimeLocalSkillImportRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.SkillKey) == "" {
		writeError(w, http.StatusBadRequest, "skill_key is required")
		return
	}

	targetSkillID := ""
	switch req.Action {
	case LocalSkillImportActionCreate:
		// nothing extra
	case LocalSkillImportActionOverwrite:
		// Existence + creator permission are re-verified authoritatively at
		// report time (the skill may change between confirm and write); here we
		// only require a well-formed target so we never enqueue a doomed write.
		uuid, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(req.TargetSkillID), "target_skill_id")
		if !ok {
			return
		}
		targetSkillID = uuidToString(uuid)
	default:
		writeError(w, http.StatusBadRequest, "invalid action")
		return
	}

	input := LocalSkillImportRequestInput{
		RequestID:     uuidToString(requestID),
		RuntimeID:     uuidToString(rt.ID),
		CreatorID:     creatorID,
		SkillKey:      strings.TrimSpace(req.SkillKey),
		Name:          cleanOptionalString(req.Name),
		Description:   cleanOptionalString(req.Description),
		Action:        req.Action,
		TargetSkillID: targetSkillID,
	}
	requestHash, err := hashRequestFingerprint(input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint local skill import")
		return
	}
	input.RequestHash = requestHash
	importReq, err := h.LocalSkillImportStore.Create(r.Context(), input)
	if err != nil {
		if errors.Is(err, errLocalSkillImportRequestConflict) {
			writeIdempotencyConflict(w, "Idempotency-Key was already used with a different request")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to enqueue local skill import: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, importReq)
}

func (h *Handler) GetLocalSkillImportRequest(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireRuntimeLocalSkillAccess(w, r, runtimeID)
	if !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")
	req, err := h.LocalSkillImportStore.Get(r.Context(), requestID)
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

func (h *Handler) ReportLocalSkillListResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")
	req, err := h.LocalSkillListStore.Get(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load request: "+err.Error())
		return
	}
	if req == nil || req.RuntimeID != runtimeID {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	if runtimeAsyncRequestTerminal(req.Status) {
		slog.Debug("ignoring stale runtime local skills report", "runtime_id", runtimeID, "request_id", requestID, "status", req.Status)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	var body struct {
		Status    string                     `json:"status"`
		Skills    []RuntimeLocalSkillSummary `json:"skills"`
		Supported *bool                      `json:"supported"`
		Error     string                     `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Status == "completed" {
		supported := true
		if body.Supported != nil {
			supported = *body.Supported
		}
		if err := h.LocalSkillListStore.Complete(r.Context(), requestID, body.Skills, supported); err != nil {
			// Surface the store failure as 5xx so the daemon can retry instead
			// of swallowing the report (leaves the request stuck in running
			// until the server-side timeout, which is exactly the "looks OK but
			// nothing happens" class of bug we're trying to avoid).
			slog.Error("local skills Complete failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist completion")
			return
		}
	} else {
		if err := h.LocalSkillListStore.Fail(r.Context(), requestID, body.Error); err != nil {
			slog.Error("local skills Fail failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist failure")
			return
		}
	}

	slog.Debug("runtime local skills report", "runtime_id", runtimeID, "request_id", requestID, "status", body.Status, "count", len(body.Skills))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) ReportLocalSkillImportResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")
	req, err := h.LocalSkillImportStore.Get(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load request: "+err.Error())
		return
	}
	if req == nil || req.RuntimeID != runtimeID {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	if runtimeAsyncRequestTerminal(req.Status) {
		slog.Debug("ignoring stale runtime local skill import report", "runtime_id", runtimeID, "request_id", requestID, "status", req.Status)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	var body struct {
		Status string                     `json:"status"`
		Skill  *reportedRuntimeLocalSkill `json:"skill"`
		Error  string                     `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Status != "completed" {
		h.failLocalSkillImport(w, r, requestID, body.Error)
		return
	}
	if body.Skill == nil {
		h.failLocalSkillImport(w, r, requestID, "daemon returned an empty skill bundle")
		return
	}
	creatorUUID, err := util.ParseUUID(req.CreatorID)
	if err != nil {
		failMsg := "stored local skill import creator_id is invalid"
		if ferr := h.LocalSkillImportStore.Fail(r.Context(), requestID, failMsg); ferr != nil {
			slog.Error("local skill import Fail failed", "error", ferr, "request_id", requestID)
		}
		writeError(w, http.StatusInternalServerError, failMsg)
		return
	}

	name := body.Skill.Name
	if req.Name != nil {
		name = *req.Name
	}
	description := body.Skill.Description
	if req.Description != nil {
		description = *req.Description
	}

	files := make([]CreateSkillFileRequest, 0, len(body.Skill.Files))
	for _, f := range body.Skill.Files {
		if !validateFilePath(f.Path) {
			continue
		}
		files = append(files, f)
	}

	config := map[string]any{
		"origin": map[string]any{
			"type":        "runtime_local",
			"runtime_id":  runtimeID,
			"provider":    body.Skill.Provider,
			"source_path": body.Skill.SourcePath,
		},
	}

	// Overwrite path: re-import onto an existing skill. Existence and creator
	// permission are re-verified inside overwriteSkillWithFiles, in the same tx
	// as the write, so a target deleted (or a creator change) between the user's
	// confirm and this report fails cleanly without falling back to create.
	if req.Action == LocalSkillImportActionOverwrite {
		targetUUID, perr := util.ParseUUID(req.TargetSkillID)
		if perr != nil {
			failMsg := "stored target_skill_id is invalid"
			if ferr := h.LocalSkillImportStore.Fail(r.Context(), requestID, failMsg); ferr != nil {
				slog.Error("local skill import Fail failed", "error", ferr, "request_id", requestID)
			}
			writeError(w, http.StatusInternalServerError, failMsg)
			return
		}
		resp, oerr := h.overwriteSkillWithFiles(r.Context(), skillOverwriteInput{
			WorkspaceID:   rt.WorkspaceID,
			TargetSkillID: targetUUID,
			UserID:        req.CreatorID,
			ExpectedName:  sanitizePostgresText(name),
			Description:   description,
			Content:       body.Skill.Content,
			Config:        config,
			Files:         files,
		})
		if oerr != nil {
			failMsg := oerr.Error()
			switch {
			case errors.Is(oerr, errSkillOverwriteNotFound):
				failMsg = "target skill no longer exists"
			case errors.Is(oerr, errSkillOverwriteForbidden):
				failMsg = "you no longer have permission to overwrite this skill"
			case errors.Is(oerr, errSkillOverwriteNameMismatch):
				failMsg = "target skill name no longer matches the imported skill"
			}
			h.failLocalSkillImport(w, r, requestID, failMsg)
			return
		}
		if err := h.LocalSkillImportStore.Complete(r.Context(), requestID, resp.SkillResponse); err != nil {
			// The overwrite already committed; unlike the create path we must
			// NOT delete the skill to "roll back" (that would destroy a
			// pre-existing skill and its agent bindings). Surface 5xx so the
			// daemon retries — the retry re-applies the same UPDATE idempotently.
			slog.Error("local skill import overwrite Complete failed",
				"error", err, "request_id", requestID, "skill_id", resp.ID)
			writeError(w, http.StatusInternalServerError, "failed to persist import completion")
			return
		}
		h.publish(protocol.EventSkillUpdated, uuidToString(rt.WorkspaceID), "member", req.CreatorID, map[string]any{"skill": resp})
		slog.Debug("runtime local skill overwritten", "runtime_id", runtimeID, "request_id", requestID, "skill_id", resp.ID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	// Create path: a same-name collision is a structured terminal state so the
	// caller can offer overwrite / rename / skip.
	if existing, found, lerr := h.lookupSkillByName(r.Context(), rt.WorkspaceID, sanitizePostgresText(name)); lerr != nil {
		h.failLocalSkillImport(w, r, requestID, "failed to check for existing skill: "+lerr.Error())
		return
	} else if found {
		h.reportLocalSkillConflict(w, r, req.ID, req.CreatorID, existing)
		return
	}

	resp, err := h.createSkillWithFiles(r.Context(), skillCreateInput{
		WorkspaceID: rt.WorkspaceID,
		CreatorID:   creatorUUID,
		Name:        name,
		Description: description,
		Content:     body.Skill.Content,
		Config:      config,
		Files:       files,
	})
	if err != nil {
		// A unique-violation here means another import won the race between our
		// lookup and the insert — surface it as a conflict, not a hard failure.
		if isUniqueViolation(err) {
			if existing, found, lerr := h.lookupSkillByName(r.Context(), rt.WorkspaceID, sanitizePostgresText(name)); lerr == nil && found {
				h.reportLocalSkillConflict(w, r, req.ID, req.CreatorID, existing)
				return
			}
			// Lost the row again (deleted between insert-fail and re-lookup), so
			// report the insert conflict without inventing metadata.
			h.failLocalSkillImport(w, r, requestID, "a skill with this name already exists")
			return
		}
		h.failLocalSkillImport(w, r, requestID, err.Error())
		return
	}

	if err := h.LocalSkillImportStore.Complete(r.Context(), requestID, resp.SkillResponse); err != nil {
		// We already wrote the Skill to Postgres. If the store-side Complete
		// fails we can't leave that Skill orphaned: the daemon will retry on
		// 5xx and re-create it, which blows up on the unique-name constraint
		// and looks to the user like "import keeps failing". Roll back our
		// side-effects so the retry lands on a clean slate.
		slog.Error("local skill import Complete failed — rolling back created skill",
			"error", err, "request_id", requestID, "skill_id", resp.ID)
		if delErr := h.Queries.DeleteSkill(r.Context(), db.DeleteSkillParams{
			ID:          parseUUID(resp.ID),
			WorkspaceID: rt.WorkspaceID,
		}); delErr != nil {
			slog.Warn("orphan skill rollback failed", "error", delErr, "skill_id", resp.ID)
		}
		writeError(w, http.StatusInternalServerError, "failed to persist import completion")
		return
	}
	h.publish(protocol.EventSkillCreated, uuidToString(rt.WorkspaceID), "member", req.CreatorID, map[string]any{"skill": resp})
	slog.Debug("runtime local skill imported", "runtime_id", runtimeID, "request_id", requestID, "skill_id", resp.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// failLocalSkillImport marks the request failed and writes the standard daemon
// response (200 ok). If the store write itself fails it returns 500 so the
// daemon retries.
func (h *Handler) failLocalSkillImport(w http.ResponseWriter, r *http.Request, requestID, failMsg string) {
	if err := h.LocalSkillImportStore.Fail(r.Context(), requestID, failMsg); err != nil {
		slog.Error("local skill import Fail failed", "error", err, "request_id", requestID)
		writeError(w, http.StatusInternalServerError, "failed to persist failure")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// reportLocalSkillConflict records a same-name conflict as the terminal
// shared conflict state with structured metadata the caller uses to
// offer overwrite / rename / skip.
func (h *Handler) reportLocalSkillConflict(w http.ResponseWriter, r *http.Request, requestID, creatorID string, existing db.Skill) {
	info := LocalSkillImportConflict{
		ExistingSkillID: uuidToString(existing.ID),
		CanOverwrite:    canOverwriteSkillByLocalImport(creatorID, existing),
	}
	if existing.CreatedBy.Valid {
		info.ExistingCreatedBy = uuidToString(existing.CreatedBy)
	}
	if err := h.LocalSkillImportStore.Conflict(r.Context(), requestID, info); err != nil {
		slog.Error("local skill import Conflict failed", "error", err, "request_id", requestID)
		writeError(w, http.StatusInternalServerError, "failed to persist conflict")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// lookupSkillByName resolves a skill by (workspace, name). found=false with a
// nil error means there is no such skill — i.e. no conflict.
func (h *Handler) lookupSkillByName(ctx context.Context, workspaceID pgtype.UUID, name string) (db.Skill, bool, error) {
	skill, err := h.Queries.GetSkillByWorkspaceAndName(ctx, db.GetSkillByWorkspaceAndNameParams{
		WorkspaceID: workspaceID,
		Name:        name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Skill{}, false, nil
		}
		return db.Skill{}, false, err
	}
	return skill, true, nil
}
