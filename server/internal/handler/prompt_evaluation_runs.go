package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func loadPromptEvaluationRun(
	w http.ResponseWriter,
	r *http.Request,
	queries *db.Queries,
	workspaceID, runID pgtype.UUID,
) (db.PromptEvaluationRun, bool) {
	run, err := queries.GetPromptEvaluationRunInWorkspace(r.Context(), db.GetPromptEvaluationRunInWorkspaceParams{
		ID: runID, WorkspaceID: workspaceID,
	})
	if err == nil {
		return run, true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "prompt evaluation run not found")
		return db.PromptEvaluationRun{}, false
	}
	writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation run")
	return db.PromptEvaluationRun{}, false
}

type promptEvaluationRunScope struct {
	workspaceID   string
	workspaceUUID pgtype.UUID
	runID         pgtype.UUID
}

func (h *Handler) parsePromptEvaluationRunScope(w http.ResponseWriter, r *http.Request) (promptEvaluationRunScope, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return promptEvaluationRunScope{}, false
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation run id")
	if !ok {
		return promptEvaluationRunScope{}, false
	}
	return promptEvaluationRunScope{workspaceID: workspaceID, workspaceUUID: workspaceUUID, runID: runID}, true
}

func (h *Handler) ListPromptEvaluationRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	assetID, ok := parseOptionalUUIDOrBadRequest(w, r.URL.Query().Get("asset_id"), "asset_id")
	if !ok {
		return
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		status = pgtype.Text{String: value, Valid: true}
	}
	var since pgtype.Timestamptz
	if value := r.URL.Query().Get("since"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		since = pgtype.Timestamptz{Time: parsed, Valid: true}
	}
	limit := int32(50)
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = int32(parsed)
	}
	offset := int32(0)
	if value := r.URL.Query().Get("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "offset must be >= 0")
			return
		}
		offset = int32(parsed)
	}
	runs, err := h.Queries.ListPromptEvaluationRuns(r.Context(), db.ListPromptEvaluationRunsParams{
		WorkspaceID: workspaceUUID,
		AssetID:     assetID,
		Status:      status,
		Since:       since,
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		slog.Error(
			"failed to list prompt evaluation runs",
			"workspace_id", workspaceID,
			"asset_id", uuidToString(assetID),
			"status", status.String,
			"since", since.Time,
			"limit", limit,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation runs")
		return
	}
	resp := make([]PromptEvaluationRunResponse, len(runs))
	for i, run := range runs {
		resp[i] = promptEvaluationRunToResponse(run)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) GetPromptEvaluationSummary(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var since pgtype.Timestamptz
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		since = pgtype.Timestamptz{Time: parsed, Valid: true}
	}
	row, err := h.Queries.GetPromptEvaluationSummary(r.Context(), db.GetPromptEvaluationSummaryParams{
		WorkspaceID: workspaceUUID,
		Since:       since,
	})
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		slog.Error("failed to load prompt evaluation summary",
			"workspace_id", workspaceID,
			"since", timestampToString(since),
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation summary")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationSummaryToResponse(workspaceUUID, row))
}

func (h *Handler) GetPromptEvaluationRuntimeReadiness(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}
	readiness, err := h.promptEvaluationRuntimeReadiness(r.Context(), workspaceUUID, member)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check training evaluation runtime readiness")
		return
	}
	writeJSON(w, http.StatusOK, readiness)
}

func (h *Handler) ListPromptEvaluationRunTrials(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.parsePromptEvaluationRunScope(w, r)
	if !ok {
		return
	}
	if _, ok := loadPromptEvaluationRun(w, r, h.Queries, scope.workspaceUUID, scope.runID); !ok {
		return
	}
	trials, err := h.Queries.ListPromptEvaluationTrialsByRun(r.Context(), db.ListPromptEvaluationTrialsByRunParams{
		RunID:       scope.runID,
		WorkspaceID: scope.workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation trials")
		return
	}
	resp := make([]PromptEvaluationTrialResponse, len(trials))
	for i, trial := range trials {
		resp[i] = promptEvaluationTrialToResponse(trial)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) GetPromptEvaluationRunEvidence(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.parsePromptEvaluationRunScope(w, r)
	if !ok {
		return
	}
	resp, err := h.buildPromptEvaluationRunEvidenceResponse(r.Context(), scope.workspaceUUID, scope.runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation run evidence")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) CancelPromptEvaluationRun(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	scope, ok := h.parsePromptEvaluationRunScope(w, r)
	if !ok {
		return
	}
	run, ok := loadPromptEvaluationRun(w, r, h.Queries, scope.workspaceUUID, scope.runID)
	if !ok {
		return
	}
	if run.Status == "已取消" {
		writeJSON(w, http.StatusOK, promptEvaluationRunToResponse(run))
		return
	}
	if run.Status != "已入队" && run.Status != "运行中" {
		writeError(w, http.StatusConflict, "only queued or running prompt evaluation runs can be cancelled")
		return
	}
	if run.TaskID.Valid {
		if !h.canCancelPromptEvaluationTask(w, r, userID, scope.workspaceID, scope.workspaceUUID, run.TaskID) {
			return
		}
		if _, err := h.TaskService.CancelTaskWithResult(r.Context(), run.TaskID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation cancellation transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	if err := qtx.MarkPromptEvaluationTrialsSkippedByRun(r.Context(), db.MarkPromptEvaluationTrialsSkippedByRunParams{
		RunID:       run.ID,
		WorkspaceID: scope.workspaceUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark prompt evaluation trials skipped")
		return
	}
	cancelled, err := qtx.CancelPromptEvaluationRun(r.Context(), db.CancelPromptEvaluationRunParams{
		ID:          run.ID,
		WorkspaceID: scope.workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			reloaded, loadErr := h.Queries.GetPromptEvaluationRunInWorkspace(r.Context(), db.GetPromptEvaluationRunInWorkspaceParams{ID: run.ID, WorkspaceID: scope.workspaceUUID})
			if loadErr == nil && reloaded.Status == "已取消" {
				writeJSON(w, http.StatusOK, promptEvaluationRunToResponse(reloaded))
				return
			}
			writeError(w, http.StatusConflict, "prompt evaluation run is no longer cancellable")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to cancel prompt evaluation run")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation cancellation")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationRunToResponse(cancelled))
}

func (h *Handler) ReviewPromptEvaluationRun(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	scope, ok := h.parsePromptEvaluationRunScope(w, r)
	if !ok {
		return
	}
	var req ReviewPromptEvaluationRunRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid review payload")
			return
		}
	}
	decision := strings.TrimSpace(req.Decision)
	if decision != "通过" && decision != "未通过" {
		writeError(w, http.StatusBadRequest, "decision must be 通过 or 未通过")
		return
	}
	note := strings.TrimSpace(req.Note)
	run, ok := loadPromptEvaluationRun(w, r, h.Queries, scope.workspaceUUID, scope.runID)
	if !ok {
		return
	}
	if run.Status == decision && run.ReviewDecision == decision && run.ReviewNote == note && uuidToString(run.ReviewedBy) == userID {
		writeJSON(w, http.StatusOK, promptEvaluationRunToResponse(run))
		return
	}
	if run.Status != "需人工复核" {
		writeError(w, http.StatusConflict, "only prompt evaluation runs requiring manual review can be reviewed")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation review transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	reviewed, err := qtx.ReviewPromptEvaluationRun(r.Context(), db.ReviewPromptEvaluationRunParams{
		ID:          run.ID,
		WorkspaceID: scope.workspaceUUID,
		Status:      decision,
		ReviewedBy:  parseUUID(userID),
		Note:        pgtype.Text{String: note, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "prompt evaluation run is no longer waiting for manual review")
			return
		}
		slog.Error("failed to review prompt evaluation run", "run_id", uuidToString(run.ID), "workspace_id", scope.workspaceID, "user_id", userID, "decision", decision, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to review prompt evaluation run")
		return
	}
	if err := qtx.MarkPromptEvaluationReviewTrialsByRun(r.Context(), db.MarkPromptEvaluationReviewTrialsByRunParams{
		RunID:       run.ID,
		WorkspaceID: scope.workspaceUUID,
		Status:      decision,
		Note:        pgtype.Text{String: note, Valid: true},
	}); err != nil {
		slog.Error("failed to review prompt evaluation trials", "run_id", uuidToString(run.ID), "workspace_id", scope.workspaceID, "decision", decision, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to review prompt evaluation trials")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Error("failed to commit prompt evaluation review", "run_id", uuidToString(run.ID), "workspace_id", scope.workspaceID, "decision", decision, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation review")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationRunToResponse(reviewed))
}

func (h *Handler) canCancelPromptEvaluationTask(w http.ResponseWriter, r *http.Request, userID, workspaceID string, workspaceUUID pgtype.UUID, taskID pgtype.UUID) bool {
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          taskID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeEntityLoadError(w, r, err, "task", "task_id", uuidToString(taskID))
		return false
	}
	if task.ChatSessionID.Valid {
		cs, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
			ID:          task.ChatSessionID,
			WorkspaceID: workspaceUUID,
		})
		if err != nil {
			writeEntityLoadError(
				w, r, err, "task",
				"task_id", uuidToString(taskID),
				"dependency", "chat_session",
				"chat_session_id", uuidToString(task.ChatSessionID),
			)
			return false
		}
		if uuidToString(cs.CreatorID) != userID {
			writeError(w, http.StatusForbidden, "not your task")
			return false
		}
		return true
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          task.AgentID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeEntityLoadError(
			w, r, err, "task",
			"task_id", uuidToString(taskID),
			"dependency", "agent",
			"agent_id", uuidToString(task.AgentID),
		)
		return false
	}
	actorType, actorID := resolveActor(r, userID)
	return h.requirePersonalAgentAccess(w, r, agent, actorType, actorID, workspaceID, "you do not have access to this agent")
}

func (h *Handler) buildPromptEvaluationRunEvidenceResponse(ctx context.Context, workspaceUUID pgtype.UUID, runID pgtype.UUID) (PromptEvaluationRunEvidenceResponse, error) {
	run, err := h.Queries.GetPromptEvaluationRunInWorkspace(ctx, db.GetPromptEvaluationRunInWorkspaceParams{ID: runID, WorkspaceID: workspaceUUID})
	if err != nil {
		return PromptEvaluationRunEvidenceResponse{}, err
	}
	trials, err := h.Queries.ListPromptEvaluationTrialsByRun(ctx, db.ListPromptEvaluationTrialsByRunParams{
		RunID:       run.ID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		return PromptEvaluationRunEvidenceResponse{}, err
	}
	trialResp := make([]PromptEvaluationTrialResponse, len(trials))
	for i, trial := range trials {
		trialResp[i] = promptEvaluationTrialToResponse(trial)
	}

	usageResp := []PromptEvaluationTaskUsageResponse{}
	messageResp := []protocol.TaskMessagePayload{}
	traceResp := []TaskTraceEventResponse{}
	var task *db.AgentTaskQueue
	if run.TaskID.Valid {
		usages, err := h.Queries.GetTaskUsage(ctx, run.TaskID)
		if err != nil {
			return PromptEvaluationRunEvidenceResponse{}, err
		}
		usageResp = make([]PromptEvaluationTaskUsageResponse, len(usages))
		for i, usage := range usages {
			usageResp[i] = promptEvaluationTaskUsageToResponse(usage)
		}

		messages, err := h.Queries.ListTaskMessages(ctx, run.TaskID)
		if err != nil {
			return PromptEvaluationRunEvidenceResponse{}, err
		}
		loadedTask, err := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: run.TaskID, WorkspaceID: workspaceUUID})
		if err != nil {
			return PromptEvaluationRunEvidenceResponse{}, err
		}
		task = &loadedTask
		issueID := uuidToString(loadedTask.IssueID)
		messageResp = make([]protocol.TaskMessagePayload, len(messages))
		for i, message := range messages {
			messageResp[i] = taskMessageToPayload(message, uuidToString(run.TaskID), issueID)
		}

		traceEvents, err := h.Queries.ListTaskTraceEventsByTask(ctx, run.TaskID)
		if err != nil {
			return PromptEvaluationRunEvidenceResponse{}, err
		}
		traceResp, err = taskTraceEventsToResponse(traceEvents)
		if err != nil {
			return PromptEvaluationRunEvidenceResponse{}, err
		}
	}
	refs, err := h.loadPromptEvaluationEvidenceRefs(ctx, workspaceUUID, run, task, traceResp)
	if err != nil {
		return PromptEvaluationRunEvidenceResponse{}, err
	}
	executionSpans, toolCallChains, toolCallSummary, executionSummary := buildPromptEvaluationExecutionEvidence(promptEvaluationRunToResponse(run), usageResp, messageResp, traceResp)

	return PromptEvaluationRunEvidenceResponse{
		Run:              promptEvaluationRunToResponse(run),
		Trials:           trialResp,
		TaskUsage:        usageResp,
		TaskMessages:     messageResp,
		TraceEvents:      traceResp,
		ExecutionSpans:   executionSpans,
		ToolCallChains:   toolCallChains,
		ToolCallSummary:  toolCallSummary,
		ExecutionSummary: executionSummary,
		Evidence:         mustDecodePersistedJSONObject(run.Evidence, "prompt evaluation run evidence"),
		Context:          buildPromptEvaluationEvidenceContext(run, task, refs, trialResp, usageResp, messageResp, traceResp),
	}, nil
}

func (h *Handler) ListPromptEvaluationEvidenceSnapshots(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.parsePromptEvaluationRunScope(w, r)
	if !ok {
		return
	}
	if _, ok := loadPromptEvaluationRun(w, r, h.Queries, scope.workspaceUUID, scope.runID); !ok {
		return
	}
	limit := int32(20)
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = int32(parsed)
	}
	items, err := h.Queries.ListPromptEvaluationEvidenceSnapshotsByRun(r.Context(), db.ListPromptEvaluationEvidenceSnapshotsByRunParams{
		WorkspaceID: scope.workspaceUUID,
		RunID:       scope.runID,
		Limit:       limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation evidence snapshots")
		return
	}
	resp := make([]PromptEvaluationEvidenceSnapshotResponse, len(items))
	for i, item := range items {
		resp[i] = promptEvaluationEvidenceSnapshotListRowToResponse(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) CreatePromptEvaluationEvidenceSnapshot(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation run id")
	if !ok {
		return
	}
	snapshotType := strings.TrimSpace(r.URL.Query().Get("snapshot_type"))
	if snapshotType == "" {
		snapshotType = "手动归档"
	}
	if !validPromptEvaluationEvidenceSnapshotType(snapshotType) {
		writeError(w, http.StatusBadRequest, "snapshot_type must be 手动归档, 验收归档 or 自动归档")
		return
	}
	actorID := parseUUID(userID)
	requestHash, err := hashRequestFingerprint(struct {
		RunID        string `json:"run_id"`
		SnapshotType string `json:"snapshot_type"`
	}{RunID: uuidToString(runID), SnapshotType: snapshotType})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint prompt evaluation evidence snapshot")
		return
	}
	idempotencyKey, ok := optionalIdempotencyKey(w, r)
	if !ok {
		return
	}
	replay, found, replayErr := loadResourceCreateReplay(
		r.Context(), h.Queries, workspaceUUID, actorID, resourceTypePromptEvidenceSnapshot,
		idempotencyKey, requestHash,
		func(response PromptEvaluationEvidenceSnapshotResponse) bool { return response.ID != "" },
	)
	if replayErr != nil {
		writePromptEvaluationEvidenceSnapshotReplayError(w, replayErr)
		return
	}
	if found {
		writeJSON(w, http.StatusCreated, replay)
		return
	}
	params, err := h.buildPromptEvaluationEvidenceSnapshotRecord(r.Context(), workspaceUUID, runID, snapshotType, actorID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation evidence snapshot")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation evidence snapshot transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	err = reserveResourceCreateRequest(r.Context(), qtx, workspaceUUID, actorID, resourceTypePromptEvidenceSnapshot, idempotencyKey, requestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(r.Context())
		replay, found, replayErr = loadResourceCreateReplay(
			r.Context(), h.Queries, workspaceUUID, actorID, resourceTypePromptEvidenceSnapshot,
			idempotencyKey, requestHash,
			func(response PromptEvaluationEvidenceSnapshotResponse) bool { return response.ID != "" },
		)
		if replayErr != nil || !found {
			if replayErr == nil {
				replayErr = errors.New("prompt evaluation evidence snapshot replay disappeared after conflict")
			}
			writePromptEvaluationEvidenceSnapshotReplayError(w, replayErr)
			return
		}
		writeJSON(w, http.StatusCreated, replay)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reserve prompt evaluation evidence snapshot request")
		return
	}
	item, err := qtx.CreatePromptEvaluationEvidenceSnapshot(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation evidence snapshot")
		return
	}
	response := promptEvaluationEvidenceSnapshotToResponse(item, true)
	if err := completeResourceCreateRequest(
		r.Context(), qtx, workspaceUUID, actorID, resourceTypePromptEvidenceSnapshot,
		idempotencyKey, requestHash, item.ID, response,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete prompt evaluation evidence snapshot request")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation evidence snapshot")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func writePromptEvaluationEvidenceSnapshotReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different evidence snapshot request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover prompt evaluation evidence snapshot request")
}

func (h *Handler) CreatePromptEvaluationAssetEvidenceSnapshots(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	snapshotType, limit, ok := parsePromptEvaluationAssetSnapshotQuery(w, r.URL.Query())
	if !ok {
		return
	}
	actorID := parseUUID(userID)
	requestHash, err := hashRequestFingerprint(struct {
		AssetID      string `json:"asset_id"`
		SnapshotType string `json:"snapshot_type"`
		Limit        int32  `json:"limit"`
	}{AssetID: uuidToString(asset.ID), SnapshotType: snapshotType, Limit: limit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint asset evidence snapshot request")
		return
	}
	idempotencyKey, ok := optionalIdempotencyKey(w, r)
	if !ok {
		return
	}
	replay, found, replayErr := loadResourceCreateReplay(
		r.Context(), h.Queries, asset.WorkspaceID, actorID, resourceTypePromptEvidenceBatch,
		idempotencyKey, requestHash,
		func(response PromptEvaluationAssetEvidenceSnapshotResponse) bool { return response.AssetID != "" },
	)
	if replayErr != nil {
		writePromptEvaluationEvidenceBatchReplayError(w, replayErr)
		return
	}
	if found {
		writeJSON(w, http.StatusCreated, replay)
		return
	}
	runs, err := h.Queries.ListPromptEvaluationRuns(r.Context(), db.ListPromptEvaluationRunsParams{
		WorkspaceID: asset.WorkspaceID,
		Limit:       limit,
		AssetID:     asset.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation runs for asset")
		return
	}
	createdBy := actorID
	paramsByRun := make(map[pgtype.UUID]db.CreatePromptEvaluationEvidenceSnapshotParams, len(runs))
	for _, run := range runs {
		existing, err := h.Queries.ListPromptEvaluationEvidenceSnapshotsByRun(r.Context(), db.ListPromptEvaluationEvidenceSnapshotsByRunParams{
			WorkspaceID: asset.WorkspaceID,
			RunID:       run.ID,
			Limit:       100,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list existing prompt evaluation evidence snapshots")
			return
		}
		if hasPromptEvaluationSnapshotType(existing, snapshotType) {
			continue
		}
		params, err := h.buildPromptEvaluationEvidenceSnapshotRecord(r.Context(), asset.WorkspaceID, run.ID, snapshotType, createdBy)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to build prompt evaluation evidence snapshot")
			return
		}
		paramsByRun[run.ID] = params
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start asset evidence snapshot transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	err = reserveResourceCreateRequest(r.Context(), qtx, asset.WorkspaceID, actorID, resourceTypePromptEvidenceBatch, idempotencyKey, requestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(r.Context())
		replay, found, replayErr = loadResourceCreateReplay(
			r.Context(), h.Queries, asset.WorkspaceID, actorID, resourceTypePromptEvidenceBatch,
			idempotencyKey, requestHash,
			func(response PromptEvaluationAssetEvidenceSnapshotResponse) bool { return response.AssetID != "" },
		)
		if replayErr != nil || !found {
			if replayErr == nil {
				replayErr = errors.New("asset evidence snapshot replay disappeared after conflict")
			}
			writePromptEvaluationEvidenceBatchReplayError(w, replayErr)
			return
		}
		writeJSON(w, http.StatusCreated, replay)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reserve asset evidence snapshot request")
		return
	}
	if _, err := qtx.LockPromptEvaluationAsset(r.Context(), db.LockPromptEvaluationAssetParams{
		ID: asset.ID, WorkspaceID: asset.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock evidence snapshot asset")
		return
	}
	resp := PromptEvaluationAssetEvidenceSnapshotResponse{
		AssetID:      uuidToString(asset.ID),
		SnapshotType: snapshotType,
		TotalRuns:    len(runs),
		Items:        []PromptEvaluationEvidenceSnapshotResponse{},
		Skipped:      []PromptEvaluationAssetEvidenceSnapshotSkip{},
	}
	for _, run := range runs {
		runID := run.ID
		existing, err := qtx.ListPromptEvaluationEvidenceSnapshotsByRun(r.Context(), db.ListPromptEvaluationEvidenceSnapshotsByRunParams{
			WorkspaceID: asset.WorkspaceID,
			RunID:       runID,
			Limit:       100,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list existing prompt evaluation evidence snapshots")
			return
		}
		if hasPromptEvaluationSnapshotType(existing, snapshotType) {
			resp.SkippedCount++
			resp.Skipped = append(resp.Skipped, PromptEvaluationAssetEvidenceSnapshotSkip{
				RunID:  uuidToString(runID),
				Reason: "已存在同类型服务端证据快照",
			})
			continue
		}
		item, err := qtx.CreatePromptEvaluationEvidenceSnapshot(r.Context(), paramsByRun[runID])
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation evidence snapshot")
			return
		}
		resp.CreatedCount++
		resp.Items = append(resp.Items, promptEvaluationEvidenceSnapshotToResponse(item, false))
	}
	if err := completeResourceCreateRequest(
		r.Context(), qtx, asset.WorkspaceID, actorID, resourceTypePromptEvidenceBatch,
		idempotencyKey, requestHash, asset.ID, resp,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete asset evidence snapshot request")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit asset evidence snapshots")
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

func hasPromptEvaluationSnapshotType(items []db.ListPromptEvaluationEvidenceSnapshotsByRunRow, snapshotType string) bool {
	for _, item := range items {
		if item.SnapshotType == snapshotType {
			return true
		}
	}
	return false
}

func writePromptEvaluationEvidenceBatchReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different asset evidence snapshot request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover asset evidence snapshot request")
}

func (h *Handler) GetPromptEvaluationAssetEvidenceSnapshotPackage(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	snapshotType, limit, ok := parsePromptEvaluationAssetSnapshotQuery(w, r.URL.Query())
	if !ok {
		return
	}
	runs, err := h.Queries.ListPromptEvaluationRuns(r.Context(), db.ListPromptEvaluationRunsParams{
		WorkspaceID: asset.WorkspaceID,
		Limit:       limit,
		AssetID:     asset.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation runs for asset")
		return
	}
	items := make([]PromptEvaluationAssetEvidenceArchiveItem, 0, len(runs))
	missingRunCount := 0
	for _, run := range runs {
		rows, err := h.Queries.ListPromptEvaluationEvidenceSnapshotsByRun(r.Context(), db.ListPromptEvaluationEvidenceSnapshotsByRunParams{
			WorkspaceID: asset.WorkspaceID,
			RunID:       run.ID,
			Limit:       100,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation evidence snapshots")
			return
		}
		snapshots := make([]PromptEvaluationEvidenceSnapshotResponse, 0, len(rows))
		for _, row := range rows {
			if row.SnapshotType != snapshotType {
				continue
			}
			detail, err := h.Queries.GetPromptEvaluationEvidenceSnapshotInWorkspace(r.Context(), db.GetPromptEvaluationEvidenceSnapshotInWorkspaceParams{
				ID:          row.ID,
				WorkspaceID: asset.WorkspaceID,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation evidence snapshot")
				return
			}
			snapshots = append(snapshots, promptEvaluationEvidenceSnapshotToResponse(detail, true))
		}
		if len(snapshots) == 0 {
			missingRunCount++
			continue
		}
		items = append(items, PromptEvaluationAssetEvidenceArchiveItem{
			Run:       promptEvaluationRunToResponse(run),
			Snapshots: snapshots,
		})
	}
	now := time.Now().UTC()
	resp := PromptEvaluationAssetEvidenceArchivePackage{
		SchemaVersion:    "multica.prompt_evaluation.asset_evidence_archive.v1",
		GeneratedAt:      now.Format(time.RFC3339),
		AssetID:          uuidToString(asset.ID),
		SnapshotType:     snapshotType,
		TotalRuns:        len(runs),
		ArchivedRunCount: len(items),
		MissingRunCount:  missingRunCount,
		Asset:            promptEvaluationAssetToResponse(asset),
		Items:            items,
		ChineseSummary: map[string]any{
			"语义版本":   "multica.prompt_evaluation.asset_evidence_archive.v1",
			"生成时间":   now.Format(time.RFC3339),
			"资产名称":   asset.Name,
			"资产类型":   asset.AssetType,
			"快照类型":   snapshotType,
			"运行总数":   len(runs),
			"已归档运行数": len(items),
			"未归档运行数": missingRunCount,
			"说明":     "该归档包按资产聚合已存在的服务端证据快照；每条快照保留原始运行证据和服务端解释快照。",
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

func parsePromptEvaluationAssetSnapshotQuery(w http.ResponseWriter, values url.Values) (string, int32, bool) {
	snapshotType := strings.TrimSpace(values.Get("snapshot_type"))
	if snapshotType == "" {
		snapshotType = "验收归档"
	}
	if !validPromptEvaluationEvidenceSnapshotType(snapshotType) {
		writeError(w, http.StatusBadRequest, "snapshot_type must be 手动归档, 验收归档 or 自动归档")
		return "", 0, false
	}

	limit := int32(20)
	if value := values.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return "", 0, false
		}
		limit = int32(parsed)
	}
	return snapshotType, limit, true
}

func (h *Handler) buildPromptEvaluationEvidenceSnapshotRecord(ctx context.Context, workspaceUUID pgtype.UUID, runID pgtype.UUID, snapshotType string, createdBy pgtype.UUID) (db.CreatePromptEvaluationEvidenceSnapshotParams, error) {
	evidence, err := h.buildPromptEvaluationRunEvidenceResponse(ctx, workspaceUUID, runID)
	if err != nil {
		return db.CreatePromptEvaluationEvidenceSnapshotParams{}, err
	}
	insight, err := h.buildPromptEvaluationEvidenceSnapshotInsight(ctx, workspaceUUID, evidence)
	if err != nil {
		return db.CreatePromptEvaluationEvidenceSnapshotParams{}, err
	}
	now := time.Now().UTC()
	payload := map[string]any{
		"语义版本":    "multica.prompt_evaluation.evidence_snapshot.v1",
		"生成时间":    now.Format(time.RFC3339),
		"快照类型":    snapshotType,
		"运行证据":    evidence,
		"服务端解释快照": insight,
	}
	return db.CreatePromptEvaluationEvidenceSnapshotParams{
		WorkspaceID:   workspaceUUID,
		RunID:         runID,
		SnapshotType:  snapshotType,
		SchemaVersion: "multica.prompt_evaluation.evidence_snapshot.v1",
		Summary:       mustJSONBytes(buildPromptEvaluationEvidenceSnapshotSummary(evidence, now, insight)),
		Evidence:      mustJSONBytes(payload),
		CreatedBy:     createdBy,
	}, nil
}

func (h *Handler) GetPromptEvaluationEvidenceSnapshot(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.parsePromptEvaluationRunScope(w, r)
	if !ok {
		return
	}
	snapshotID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "snapshotId"), "prompt evaluation evidence snapshot id")
	if !ok {
		return
	}
	item, err := h.Queries.GetPromptEvaluationEvidenceSnapshotInWorkspace(r.Context(), db.GetPromptEvaluationEvidenceSnapshotInWorkspaceParams{
		ID:          snapshotID,
		WorkspaceID: scope.workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation evidence snapshot not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation evidence snapshot")
		return
	}
	if uuidToString(item.RunID) != uuidToString(scope.runID) {
		writeError(w, http.StatusNotFound, "prompt evaluation evidence snapshot not found")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationEvidenceSnapshotToResponse(item, true))
}

func (h *Handler) loadPromptEvaluationEvidenceRefs(
	ctx context.Context,
	workspaceID pgtype.UUID,
	run db.PromptEvaluationRun,
	task *db.AgentTaskQueue,
	traceEvents []TaskTraceEventResponse,
) (promptEvaluationEvidenceRefs, error) {
	refs := promptEvaluationEvidenceRefs{}
	if run.AssetID.Valid {
		asset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(ctx, db.GetPromptEvaluationAssetInWorkspaceParams{ID: run.AssetID, WorkspaceID: workspaceID})
		if err != nil {
			return refs, err
		}
		refs.Asset = &asset
	}
	if run.PromptID.Valid {
		prompt, err := h.Queries.GetPromptLibraryItemInWorkspace(ctx, db.GetPromptLibraryItemInWorkspaceParams{ID: run.PromptID, WorkspaceID: workspaceID})
		if err != nil {
			return refs, err
		}
		refs.Prompt = &prompt
	}
	if run.AgentID.Valid {
		agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: run.AgentID, WorkspaceID: workspaceID})
		if err != nil {
			return refs, err
		}
		refs.Agent = &agent
	}
	if run.RuntimeID.Valid {
		runtime, err := h.Queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID: run.RuntimeID, WorkspaceID: workspaceID})
		if err != nil {
			return refs, err
		}
		refs.Runtime = &runtime
	}

	issueID := pgtype.UUID{}
	if task != nil && task.IssueID.Valid {
		issueID = task.IssueID
	} else if id := firstTraceUUID(traceEvents, func(event TaskTraceEventResponse) *string { return event.IssueID }); id.Valid {
		issueID = id
	}
	if issueID.Valid {
		issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
		if err != nil {
			return refs, err
		}
		refs.Issue = &issue
	}

	projectID := pgtype.UUID{}
	if refs.Issue != nil && refs.Issue.ProjectID.Valid {
		projectID = refs.Issue.ProjectID
	} else if refs.Prompt != nil && refs.Prompt.ProjectID.Valid {
		projectID = refs.Prompt.ProjectID
	} else if id := firstTraceUUID(traceEvents, func(event TaskTraceEventResponse) *string { return event.ProjectID }); id.Valid {
		projectID = id
	}
	if projectID.Valid {
		project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
		if err != nil {
			return refs, err
		}
		refs.Project = &project
	}

	squadID := pgtype.UUID{}
	if refs.Issue != nil && refs.Issue.AssigneeType.Valid && refs.Issue.AssigneeType.String == "squad" && refs.Issue.AssigneeID.Valid {
		squadID = refs.Issue.AssigneeID
	} else if id := firstTraceUUID(traceEvents, func(event TaskTraceEventResponse) *string { return event.SquadID }); id.Valid {
		squadID = id
	}
	if squadID.Valid {
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: squadID, WorkspaceID: workspaceID})
		if err != nil {
			return refs, err
		}
		refs.Squad = &squad
	}
	return refs, nil
}

func (h *Handler) buildPromptEvaluationEvidenceSnapshotInsight(ctx context.Context, workspaceID pgtype.UUID, evidence PromptEvaluationRunEvidenceResponse) (map[string]any, error) {
	run := evidence.Run
	assetID := parseUUID(run.AssetID)
	summaries, err := h.Queries.ListPromptEvaluationDimensionScoreSummaries(ctx, db.ListPromptEvaluationDimensionScoreSummariesParams{
		WorkspaceID: workspaceID,
		AssetID:     assetID,
	})
	if err != nil {
		return nil, err
	}
	trends, err := h.Queries.ListPromptEvaluationDimensionScoreTrends(ctx, db.ListPromptEvaluationDimensionScoreTrendsParams{
		WorkspaceID: workspaceID,
		AssetID:     assetID,
	})
	if err != nil {
		return nil, err
	}
	candidates, err := h.Queries.ListPromptEvaluationOptimizationCandidates(ctx, db.ListPromptEvaluationOptimizationCandidatesParams{
		WorkspaceID: workspaceID,
		RunID:       parseUUID(run.ID),
		Limit:       20,
	})
	if err != nil {
		return nil, err
	}
	summaryItems := make([]PromptEvaluationDimensionScoreSummaryResponse, len(summaries))
	for i, item := range summaries {
		summaryItems[i] = promptEvaluationDimensionScoreSummaryToResponse(item)
	}
	trendItems := make([]PromptEvaluationDimensionScoreTrendResponse, len(trends))
	for i, item := range trends {
		trendItems[i] = promptEvaluationDimensionScoreTrendToResponse(item)
	}
	candidateItems := make([]map[string]any, 0, len(candidates))
	for _, item := range candidates {
		resp := promptEvaluationOptimizationCandidateToResponse(item)
		metrics, _ := resp.Metrics.(map[string]any)
		candidateItems = append(candidateItems, map[string]any{
			"id":        resp.ID,
			"run_id":    resp.RunID,
			"prompt_id": resp.PromptID,
			"状态":        resp.Status,
			"失败用例数":     resp.FailedCaseCount,
			"候选优先级":     stringFromAny(metrics["候选优先级"]),
			"失败维度":      metrics["失败维度"],
			"优先级依据":     stringFromAny(metrics["候选优先级依据"]),
			"修改依据":      resp.Rationale,
		})
	}
	return map[string]any{
		"语义版本":   "multica.prompt_evaluation.evidence_snapshot.insight.v1",
		"运行ID":   run.ID,
		"资产ID":   run.AssetID,
		"质量判断":   promptEvaluationEvidenceQualityLabel(run),
		"单位通过成本": promptEvaluationEvidenceCostPerPassedCase(run),
		"失败主因":   promptEvaluationEvidenceFailureReason(evidence),
		"建议动作":   promptEvaluationEvidenceRecommendation(evidence, len(candidateItems)),
		"维度评分摘要": summaryItems,
		"维度评分趋势": trendItems,
		"优化候选证据": candidateItems,
	}, nil
}
