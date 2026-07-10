package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) ListPromptEvaluationRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var assetID pgtype.UUID
	if value := r.URL.Query().Get("asset_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "asset_id")
		if !ok {
			return
		}
		assetID = parsed
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
	includeAcceptanceFixtures := true
	if raw := r.URL.Query().Get("include_acceptance_fixtures"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "include_acceptance_fixtures must be boolean")
			return
		}
		includeAcceptanceFixtures = parsed
	}
	row, err := h.Queries.GetPromptEvaluationSummary(r.Context(), db.GetPromptEvaluationSummaryParams{
		WorkspaceID:               workspaceUUID,
		IncludeAcceptanceFixtures: includeAcceptanceFixtures,
		Since:                     since,
	})
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		slog.Error("failed to load prompt evaluation summary",
			"workspace_id", workspaceID,
			"include_acceptance_fixtures", includeAcceptanceFixtures,
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
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation run id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetPromptEvaluationRunInWorkspace(r.Context(), db.GetPromptEvaluationRunInWorkspaceParams{ID: runID, WorkspaceID: workspaceUUID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation run")
		return
	}
	trials, err := h.Queries.ListPromptEvaluationTrialsByRun(r.Context(), db.ListPromptEvaluationTrialsByRunParams{
		RunID:       runID,
		WorkspaceID: workspaceUUID,
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
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation run id")
	if !ok {
		return
	}
	resp, err := h.buildPromptEvaluationRunEvidenceResponse(r.Context(), workspaceUUID, runID)
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
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation run id")
	if !ok {
		return
	}
	run, err := h.Queries.GetPromptEvaluationRunInWorkspace(r.Context(), db.GetPromptEvaluationRunInWorkspaceParams{ID: runID, WorkspaceID: workspaceUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation run")
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
		if !h.canCancelPromptEvaluationTask(w, r, userID, workspaceID, workspaceUUID, run.TaskID) {
			return
		}
		if _, err := h.TaskService.CancelTaskWithResult(r.Context(), run.TaskID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := h.Queries.MarkPromptEvaluationTrialsSkippedByRun(r.Context(), db.MarkPromptEvaluationTrialsSkippedByRunParams{
		RunID:       run.ID,
		WorkspaceID: workspaceUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark prompt evaluation trials skipped")
		return
	}
	cancelled, err := h.Queries.CancelPromptEvaluationRun(r.Context(), db.CancelPromptEvaluationRunParams{
		ID:          run.ID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			reloaded, loadErr := h.Queries.GetPromptEvaluationRunInWorkspace(r.Context(), db.GetPromptEvaluationRunInWorkspaceParams{ID: run.ID, WorkspaceID: workspaceUUID})
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
	writeJSON(w, http.StatusOK, promptEvaluationRunToResponse(cancelled))
}

func (h *Handler) ReviewPromptEvaluationRun(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation run id")
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
	run, err := h.Queries.GetPromptEvaluationRunInWorkspace(r.Context(), db.GetPromptEvaluationRunInWorkspaceParams{ID: runID, WorkspaceID: workspaceUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation run")
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
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	reviewed, err := qtx.ReviewPromptEvaluationRun(r.Context(), db.ReviewPromptEvaluationRunParams{
		ID:          run.ID,
		WorkspaceID: workspaceUUID,
		Status:      decision,
		ReviewedBy:  parseUUID(userID),
		Note:        pgtype.Text{String: note, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "prompt evaluation run is no longer waiting for manual review")
			return
		}
		slog.Error("failed to review prompt evaluation run", "run_id", uuidToString(run.ID), "workspace_id", workspaceID, "user_id", userID, "decision", decision, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to review prompt evaluation run")
		return
	}
	if err := qtx.MarkPromptEvaluationReviewTrialsByRun(r.Context(), db.MarkPromptEvaluationReviewTrialsByRunParams{
		RunID:       run.ID,
		WorkspaceID: workspaceUUID,
		Status:      decision,
		Note:        pgtype.Text{String: note, Valid: true},
	}); err != nil {
		slog.Error("failed to review prompt evaluation trials", "run_id", uuidToString(run.ID), "workspace_id", workspaceID, "decision", decision, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to review prompt evaluation trials")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Error("failed to commit prompt evaluation review", "run_id", uuidToString(run.ID), "workspace_id", workspaceID, "decision", decision, "error", err)
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
		writeError(w, http.StatusNotFound, "task not found")
		return false
	}
	if task.ChatSessionID.Valid {
		cs, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
			ID:          task.ChatSessionID,
			WorkspaceID: workspaceUUID,
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "task not found")
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
		writeError(w, http.StatusNotFound, "task not found")
		return false
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canAccessPersonalAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return false
	}
	return true
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
		issueID := ""
		if loadedTask, err := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: run.TaskID, WorkspaceID: workspaceUUID}); err == nil {
			task = &loadedTask
			issueID = uuidToString(loadedTask.IssueID)
		}
		messageResp = make([]protocol.TaskMessagePayload, len(messages))
		for i, message := range messages {
			messageResp[i] = taskMessageToPayload(message, uuidToString(run.TaskID), issueID)
		}

		traceEvents, err := h.Queries.ListTaskTraceEventsByTask(ctx, run.TaskID)
		if err != nil {
			return PromptEvaluationRunEvidenceResponse{}, err
		}
		traceResp = make([]TaskTraceEventResponse, len(traceEvents))
		for i, event := range traceEvents {
			traceResp[i] = taskTraceEventToResponse(event)
		}
	}
	refs := h.loadPromptEvaluationEvidenceRefs(ctx, workspaceUUID, run, task, traceResp)
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
		Evidence:         decodeJSONDefault(run.Evidence, map[string]any{}),
		Context:          buildPromptEvaluationEvidenceContext(run, task, refs, trialResp, usageResp, messageResp, traceResp),
	}, nil
}

func (h *Handler) ListPromptEvaluationEvidenceSnapshots(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation run id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetPromptEvaluationRunInWorkspace(r.Context(), db.GetPromptEvaluationRunInWorkspaceParams{ID: runID, WorkspaceID: workspaceUUID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation run")
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
		WorkspaceID: workspaceUUID,
		RunID:       runID,
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
	item, err := h.createPromptEvaluationEvidenceSnapshotRecord(r.Context(), workspaceUUID, runID, snapshotType, parseUUID(userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation evidence snapshot")
		return
	}
	writeJSON(w, http.StatusCreated, promptEvaluationEvidenceSnapshotToResponse(item, true))
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
	snapshotType := strings.TrimSpace(r.URL.Query().Get("snapshot_type"))
	if snapshotType == "" {
		snapshotType = "验收归档"
	}
	if !validPromptEvaluationEvidenceSnapshotType(snapshotType) {
		writeError(w, http.StatusBadRequest, "snapshot_type must be 手动归档, 验收归档 or 自动归档")
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
	runs, err := h.Queries.ListPromptEvaluationRuns(r.Context(), db.ListPromptEvaluationRunsParams{
		WorkspaceID: asset.WorkspaceID,
		Limit:       limit,
		AssetID:     asset.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation runs for asset")
		return
	}
	resp := PromptEvaluationAssetEvidenceSnapshotResponse{
		AssetID:      uuidToString(asset.ID),
		SnapshotType: snapshotType,
		TotalRuns:    len(runs),
		Items:        []PromptEvaluationEvidenceSnapshotResponse{},
		Skipped:      []PromptEvaluationAssetEvidenceSnapshotSkip{},
	}
	createdBy := parseUUID(userID)
	for _, run := range runs {
		runID := run.ID
		existing, err := h.Queries.ListPromptEvaluationEvidenceSnapshotsByRun(r.Context(), db.ListPromptEvaluationEvidenceSnapshotsByRunParams{
			WorkspaceID: asset.WorkspaceID,
			RunID:       runID,
			Limit:       100,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list existing prompt evaluation evidence snapshots")
			return
		}
		alreadyArchived := false
		for _, item := range existing {
			if item.SnapshotType == snapshotType {
				alreadyArchived = true
				break
			}
		}
		if alreadyArchived {
			resp.SkippedCount++
			resp.Skipped = append(resp.Skipped, PromptEvaluationAssetEvidenceSnapshotSkip{
				RunID:  uuidToString(runID),
				Reason: "已存在同类型服务端证据快照",
			})
			continue
		}
		item, err := h.createPromptEvaluationEvidenceSnapshotRecord(r.Context(), asset.WorkspaceID, runID, snapshotType, createdBy)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation evidence snapshot")
			return
		}
		resp.CreatedCount++
		resp.Items = append(resp.Items, promptEvaluationEvidenceSnapshotToResponse(item, false))
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) GetPromptEvaluationAssetEvidenceSnapshotPackage(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	snapshotType := strings.TrimSpace(r.URL.Query().Get("snapshot_type"))
	if snapshotType == "" {
		snapshotType = "验收归档"
	}
	if !validPromptEvaluationEvidenceSnapshotType(snapshotType) {
		writeError(w, http.StatusBadRequest, "snapshot_type must be 手动归档, 验收归档 or 自动归档")
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

func (h *Handler) createPromptEvaluationEvidenceSnapshotRecord(ctx context.Context, workspaceUUID pgtype.UUID, runID pgtype.UUID, snapshotType string, createdBy pgtype.UUID) (db.PromptEvaluationEvidenceSnapshot, error) {
	evidence, err := h.buildPromptEvaluationRunEvidenceResponse(ctx, workspaceUUID, runID)
	if err != nil {
		return db.PromptEvaluationEvidenceSnapshot{}, err
	}
	insight, err := h.buildPromptEvaluationEvidenceSnapshotInsight(ctx, workspaceUUID, evidence)
	if err != nil {
		return db.PromptEvaluationEvidenceSnapshot{}, err
	}
	now := time.Now().UTC()
	payload := map[string]any{
		"语义版本":    "multica.prompt_evaluation.evidence_snapshot.v1",
		"生成时间":    now.Format(time.RFC3339),
		"快照类型":    snapshotType,
		"运行证据":    evidence,
		"服务端解释快照": insight,
	}
	return h.Queries.CreatePromptEvaluationEvidenceSnapshot(ctx, db.CreatePromptEvaluationEvidenceSnapshotParams{
		WorkspaceID:   workspaceUUID,
		RunID:         runID,
		SnapshotType:  snapshotType,
		SchemaVersion: "multica.prompt_evaluation.evidence_snapshot.v1",
		Summary:       mustJSONBytes(buildPromptEvaluationEvidenceSnapshotSummary(evidence, now, insight)),
		Evidence:      mustJSONBytes(payload),
		CreatedBy:     createdBy,
	})
}

func (h *Handler) GetPromptEvaluationEvidenceSnapshot(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation run id")
	if !ok {
		return
	}
	snapshotID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "snapshotId"), "prompt evaluation evidence snapshot id")
	if !ok {
		return
	}
	item, err := h.Queries.GetPromptEvaluationEvidenceSnapshotInWorkspace(r.Context(), db.GetPromptEvaluationEvidenceSnapshotInWorkspaceParams{
		ID:          snapshotID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation evidence snapshot not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation evidence snapshot")
		return
	}
	if uuidToString(item.RunID) != uuidToString(runID) {
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
) promptEvaluationEvidenceRefs {
	refs := promptEvaluationEvidenceRefs{}
	if run.AssetID.Valid {
		if asset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(ctx, db.GetPromptEvaluationAssetInWorkspaceParams{ID: run.AssetID, WorkspaceID: workspaceID}); err == nil {
			refs.Asset = &asset
		}
	}
	if run.PromptID.Valid {
		if prompt, err := h.Queries.GetPromptLibraryItemInWorkspace(ctx, db.GetPromptLibraryItemInWorkspaceParams{ID: run.PromptID, WorkspaceID: workspaceID}); err == nil {
			refs.Prompt = &prompt
		}
	}
	if run.AgentID.Valid {
		if agent, err := h.Queries.GetAgent(ctx, run.AgentID); err == nil && uuidToString(agent.WorkspaceID) == uuidToString(workspaceID) {
			refs.Agent = &agent
		}
	}
	if run.RuntimeID.Valid {
		if runtime, err := h.Queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID: run.RuntimeID, WorkspaceID: workspaceID}); err == nil {
			refs.Runtime = &runtime
		}
	}

	issueID := pgtype.UUID{}
	if task != nil && task.IssueID.Valid {
		issueID = task.IssueID
	} else if id := firstTraceUUID(traceEvents, func(event TaskTraceEventResponse) *string { return event.IssueID }); id.Valid {
		issueID = id
	}
	if issueID.Valid {
		if issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID}); err == nil {
			refs.Issue = &issue
		}
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
		if project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID}); err == nil {
			refs.Project = &project
		}
	}

	squadID := pgtype.UUID{}
	if refs.Issue != nil && refs.Issue.AssigneeType.Valid && refs.Issue.AssigneeType.String == "squad" && refs.Issue.AssigneeID.Valid {
		squadID = refs.Issue.AssigneeID
	} else if id := firstTraceUUID(traceEvents, func(event TaskTraceEventResponse) *string { return event.SquadID }); id.Valid {
		squadID = id
	}
	if squadID.Valid {
		if squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: squadID, WorkspaceID: workspaceID}); err == nil {
			refs.Squad = &squad
		}
	}
	return refs
}

func firstTraceUUID(traceEvents []TaskTraceEventResponse, selectID func(TaskTraceEventResponse) *string) pgtype.UUID {
	for _, event := range traceEvents {
		if value := selectID(event); value != nil && strings.TrimSpace(*value) != "" {
			if id, err := util.ParseUUID(*value); err == nil {
				return id
			}
		}
	}
	return pgtype.UUID{}
}

func buildPromptEvaluationExecutionEvidence(
	run PromptEvaluationRunResponse,
	usages []PromptEvaluationTaskUsageResponse,
	messages []protocol.TaskMessagePayload,
	traceEvents []TaskTraceEventResponse,
) ([]PromptEvaluationExecutionSpanResponse, []PromptEvaluationToolCallChainResponse, []PromptEvaluationToolCallSummaryResponse, map[string]any) {
	rootID := firstNonEmptyPromptEvaluationString(ptrString(run.TaskID), run.ID)
	rootSpanID := "task:" + rootID
	toolCallChains := buildPromptEvaluationToolCallChains(messages)
	toolCallSummary := buildPromptEvaluationToolCallSummary(toolCallChains)
	toolCallChainByMessageSeq := buildPromptEvaluationToolCallChainByMessageSeq(toolCallChains)
	spans := []PromptEvaluationExecutionSpanResponse{{
		ID:         rootSpanID,
		SpanKind:   "任务根节点",
		SpanName:   "评估任务执行",
		Status:     run.Status,
		Seq:        0,
		TaskID:     ptrString(run.TaskID),
		Provider:   run.RuntimeProvider,
		Model:      run.Model,
		TokenTotal: int64(run.InputTokens + run.OutputTokens),
		DurationMs: run.TotalDurationMs,
		Summary:    firstNonEmptyPromptEvaluationString(run.Conclusion, run.FailureReason, "评估任务执行上下文"),
		Details: map[string]any{
			"运行":   run.ID,
			"运行类型": run.RunKind,
			"触发来源": run.TriggerSource,
			"通过数":  run.PassedCases,
			"失败数":  run.FailedCases,
			"预估成本": run.EstimatedCost,
		},
		CreatedAt: run.CreatedAt,
	}}

	summary := map[string]any{
		"根任务":         rootID,
		"生命周期span数":   0,
		"工具span数":     0,
		"消息span数":     0,
		"用量span数":     0,
		"trace span数": 0,
		"token标记合计":   int64(0),
		"是否缺失用量":      false,
		"工具调用链数":      len(toolCallChains),
		"已配对工具调用数":    0,
		"缺少结果工具调用数":   0,
		"孤立工具结果数":     0,
	}
	for _, chain := range toolCallChains {
		switch chain.Status {
		case "已配对":
			incrementPromptEvaluationSummary(summary, "已配对工具调用数")
		case "缺少结果":
			incrementPromptEvaluationSummary(summary, "缺少结果工具调用数")
		case "孤立结果":
			incrementPromptEvaluationSummary(summary, "孤立工具结果数")
		}
	}

	seq := 1
	for _, event := range traceEvents {
		kind := promptEvaluationTraceSpanKind(event.EventType)
		if strings.HasPrefix(event.EventType, "task.") {
			incrementPromptEvaluationSummary(summary, "生命周期span数")
		}
		if strings.HasPrefix(event.EventType, "llm.") {
			incrementPromptEvaluationSummary(summary, "用量span数")
		}
		if event.EventType == "llm.usage_unavailable" {
			summary["是否缺失用量"] = true
		}
		incrementPromptEvaluationSummary(summary, "trace span数")
		tokenTotal := int64(event.InputTokens + event.OutputTokens + event.CacheReadTokens + event.CacheWriteTokens)
		summary["token标记合计"] = summary["token标记合计"].(int64) + tokenTotal
		spans = append(spans, PromptEvaluationExecutionSpanResponse{
			ID:         fmt.Sprintf("trace:%s:%d", event.ID, seq),
			ParentID:   rootSpanID,
			SpanKind:   kind,
			SpanName:   firstNonEmptyPromptEvaluationString(event.EventName, event.EventType),
			Status:     event.Status,
			Seq:        seq,
			TaskID:     event.TaskID,
			Provider:   event.Provider,
			Model:      event.Model,
			TokenTotal: tokenTotal,
			DurationMs: promptEvaluationTraceDurationMs(event),
			Summary:    promptEvaluationTraceSpanSummary(event),
			Details: map[string]any{
				"事件类型":   event.EventType,
				"失败原因":   event.FailureReason,
				"错误类型":   event.ErrorType,
				"排队耗时ms": event.QueueWaitMs,
				"执行耗时ms": event.RunMs,
				"总耗时ms":  event.TotalMs,
				"元数据":    event.Metadata,
			},
			CreatedAt: event.CreatedAt,
		})
		seq++
	}

	for _, message := range messages {
		kind := promptEvaluationMessageSpanKind(message.Type)
		if kind == "工具调用" || kind == "工具结果" {
			incrementPromptEvaluationSummary(summary, "工具span数")
		} else {
			incrementPromptEvaluationSummary(summary, "消息span数")
		}
		details := map[string]any{
			"消息序号": message.Seq,
			"消息类型": message.Type,
			"输入":   message.Input,
			"输出":   message.Output,
		}
		if chain, ok := toolCallChainByMessageSeq[message.Seq]; ok {
			details["工具调用链ID"] = chain.ID
			details["工具调用链状态"] = chain.Status
			if chain.UseSeq > 0 {
				details["工具调用序号"] = chain.UseSeq
			}
			if chain.ResultSeq > 0 {
				details["工具结果序号"] = chain.ResultSeq
			}
		}
		spans = append(spans, PromptEvaluationExecutionSpanResponse{
			ID:        fmt.Sprintf("message:%d", message.Seq),
			ParentID:  rootSpanID,
			SpanKind:  kind,
			SpanName:  firstNonEmptyPromptEvaluationString(message.Tool, promptEvaluationMessageSpanName(message.Type)),
			Status:    "已记录",
			Seq:       seq,
			TaskID:    message.TaskID,
			Tool:      message.Tool,
			Summary:   truncatePromptEvaluationEvidence(firstNonEmptyPromptEvaluationString(message.Content, message.Output, promptEvaluationEvidenceSummaryString(message.Input), message.Type), 240),
			Details:   details,
			CreatedAt: message.CreatedAt,
		})
		seq++
	}

	for i, usage := range usages {
		tokenTotal := usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
		summary["token标记合计"] = summary["token标记合计"].(int64) + tokenTotal
		incrementPromptEvaluationSummary(summary, "用量span数")
		spans = append(spans, PromptEvaluationExecutionSpanResponse{
			ID:         fmt.Sprintf("usage:%s:%d", usage.ID, i+1),
			ParentID:   rootSpanID,
			SpanKind:   "模型用量",
			SpanName:   "模型 token 用量",
			Status:     "已计量",
			Seq:        seq,
			TaskID:     usage.TaskID,
			Provider:   usage.Provider,
			Model:      usage.Model,
			TokenTotal: tokenTotal,
			Summary:    fmt.Sprintf("输入 %d，输出 %d，缓存读 %d，缓存写 %d，预估成本 %.6f", usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.EstimatedCost),
			Details: map[string]any{
				"输入token": usage.InputTokens,
				"输出token": usage.OutputTokens,
				"缓存读":     usage.CacheReadTokens,
				"缓存写":     usage.CacheWriteTokens,
				"预估成本":    usage.EstimatedCost,
				"已定价":     usage.Priced,
			},
			CreatedAt: usage.CreatedAt,
		})
		seq++
	}

	summary["span总数"] = len(spans)
	return spans, toolCallChains, toolCallSummary, summary
}

func buildPromptEvaluationToolCallChains(messages []protocol.TaskMessagePayload) []PromptEvaluationToolCallChainResponse {
	chains := []PromptEvaluationToolCallChainResponse{}
	pendingByTool := map[string][]int{}
	for _, message := range messages {
		switch message.Type {
		case "tool_use":
			tool := strings.TrimSpace(message.Tool)
			if tool == "" {
				tool = "未记录工具"
			}
			callID := promptEvaluationToolCallID(message)
			if callID == "" {
				callID = fmt.Sprintf("%s:%d", tool, message.Seq)
			}
			chain := PromptEvaluationToolCallChainResponse{
				ID:             "tool:" + callID,
				TaskID:         message.TaskID,
				Tool:           tool,
				Status:         "缺少结果",
				UseSeq:         message.Seq,
				UseSpanID:      fmt.Sprintf("message:%d", message.Seq),
				Input:          message.Input,
				ResultCategory: "未返回",
				Summary:        truncatePromptEvaluationEvidence("工具调用："+firstNonEmptyPromptEvaluationString(tool, promptEvaluationEvidenceSummaryString(message.Input)), 240),
				CreatedAt:      message.CreatedAt,
			}
			chains = append(chains, chain)
			pendingByTool[tool] = append(pendingByTool[tool], len(chains)-1)
		case "tool_result":
			tool := strings.TrimSpace(message.Tool)
			if tool == "" {
				tool = "未记录工具"
			}
			pending := pendingByTool[tool]
			if len(pending) > 0 {
				index := pending[0]
				pendingByTool[tool] = pending[1:]
				chains[index].Status = "已配对"
				chains[index].ResultSeq = message.Seq
				chains[index].ResultSpanID = fmt.Sprintf("message:%d", message.Seq)
				chains[index].Output = message.Output
				chains[index].DurationMs = promptEvaluationDurationBetween(chains[index].CreatedAt, message.CreatedAt)
				chains[index].ResultCategory = "已返回"
				if failureSignal, failureReason := promptEvaluationToolFailureSignal(tool, message.Output); failureSignal {
					chains[index].FailureSignal = true
					chains[index].FailureReason = failureReason
					chains[index].ResultCategory = "异常线索"
				}
				chains[index].CompletedAt = message.CreatedAt
				chains[index].Summary = truncatePromptEvaluationEvidence(
					fmt.Sprintf("工具 %s 已配对：调用 #%d，结果 #%d", tool, chains[index].UseSeq, message.Seq),
					240,
				)
				continue
			}
			callID := promptEvaluationToolCallID(message)
			if callID == "" {
				callID = fmt.Sprintf("%s:result:%d", tool, message.Seq)
			}
			chains = append(chains, PromptEvaluationToolCallChainResponse{
				ID:             "tool:" + callID,
				TaskID:         message.TaskID,
				Tool:           tool,
				Status:         "孤立结果",
				ResultSeq:      message.Seq,
				ResultSpanID:   fmt.Sprintf("message:%d", message.Seq),
				Output:         message.Output,
				ResultCategory: "孤立返回",
				Summary:        truncatePromptEvaluationEvidence("工具结果没有找到对应调用："+firstNonEmptyPromptEvaluationString(message.Output, tool), 240),
				CompletedAt:    message.CreatedAt,
			})
		}
	}
	return chains
}

func buildPromptEvaluationToolCallSummary(chains []PromptEvaluationToolCallChainResponse) []PromptEvaluationToolCallSummaryResponse {
	if len(chains) == 0 {
		return []PromptEvaluationToolCallSummaryResponse{}
	}
	byTool := map[string]*PromptEvaluationToolCallSummaryResponse{}
	durationSums := map[string]int64{}
	durationCounts := map[string]int64{}
	for _, chain := range chains {
		tool := strings.TrimSpace(chain.Tool)
		if tool == "" {
			tool = "未记录工具"
		}
		item := byTool[tool]
		if item == nil {
			item = &PromptEvaluationToolCallSummaryResponse{
				Tool:             tool,
				ResultCategories: map[string]int{},
			}
			byTool[tool] = item
		}
		item.TotalCalls++
		switch chain.Status {
		case "已配对":
			item.PairedCalls++
		case "缺少结果":
			item.MissingResultCalls++
		case "孤立结果":
			item.OrphanResultCalls++
		}
		category := strings.TrimSpace(chain.ResultCategory)
		if category == "" {
			category = "未归类"
		}
		item.ResultCategories[category]++
		if chain.FailureSignal {
			item.FailureSignalCalls++
		}
		if chain.DurationMs > 0 {
			durationSums[tool] += chain.DurationMs
			durationCounts[tool]++
			if chain.DurationMs > item.MaxDurationMs {
				item.MaxDurationMs = chain.DurationMs
				item.SlowestToolCallChainID = chain.ID
			}
		}
	}
	result := make([]PromptEvaluationToolCallSummaryResponse, 0, len(byTool))
	for tool, item := range byTool {
		if count := durationCounts[tool]; count > 0 {
			item.AverageDurationMs = durationSums[tool] / count
		}
		item.NeedsAttention = item.MissingResultCalls > 0 || item.OrphanResultCalls > 0 || item.FailureSignalCalls > 0
		item.Summary = fmt.Sprintf(
			"%s：调用 %d 次，已配对 %d 次，缺少结果 %d 次，孤立结果 %d 次，异常线索 %d 次，平均耗时 %dms，最慢 %dms",
			tool,
			item.TotalCalls,
			item.PairedCalls,
			item.MissingResultCalls,
			item.OrphanResultCalls,
			item.FailureSignalCalls,
			item.AverageDurationMs,
			item.MaxDurationMs,
		)
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].NeedsAttention != result[j].NeedsAttention {
			return result[i].NeedsAttention
		}
		if result[i].MaxDurationMs != result[j].MaxDurationMs {
			return result[i].MaxDurationMs > result[j].MaxDurationMs
		}
		if result[i].TotalCalls != result[j].TotalCalls {
			return result[i].TotalCalls > result[j].TotalCalls
		}
		return result[i].Tool < result[j].Tool
	})
	return result
}

func buildPromptEvaluationToolCallChainByMessageSeq(chains []PromptEvaluationToolCallChainResponse) map[int]PromptEvaluationToolCallChainResponse {
	result := map[int]PromptEvaluationToolCallChainResponse{}
	for _, chain := range chains {
		if chain.UseSeq > 0 {
			result[chain.UseSeq] = chain
		}
		if chain.ResultSeq > 0 {
			result[chain.ResultSeq] = chain
		}
	}
	return result
}

func promptEvaluationDurationBetween(start string, end string) int64 {
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	if start == "" || end == "" {
		return 0
	}
	startAt, startErr := time.Parse(time.RFC3339Nano, start)
	endAt, endErr := time.Parse(time.RFC3339Nano, end)
	if startErr != nil || endErr != nil || endAt.Before(startAt) {
		return 0
	}
	return endAt.Sub(startAt).Milliseconds()
}

func promptEvaluationToolFailureSignal(tool string, output string) (bool, string) {
	displayOutput := promptEvaluationToolOutputText(output)
	normalized := strings.ToLower(strings.TrimSpace(displayOutput))
	if normalized == "" {
		return false, ""
	}
	if promptEvaluationToolOutputHasToolUseError(normalized) {
		return true, "工具调用返回错误"
	}
	if exitCode, ok := promptEvaluationToolExitCode(normalized); ok {
		if exitCode == 0 {
			return false, ""
		}
		return true, fmt.Sprintf("工具结果包含非零退出码 %d", exitCode)
	}
	if statusCode := promptEvaluationToolHTTPStatusCode(normalized); statusCode >= 400 {
		return true, fmt.Sprintf("工具结果包含 HTTP 状态码 %d", statusCode)
	}
	if promptEvaluationToolResultIsContentOnly(tool) || promptEvaluationToolOutputIsReadOnlyCommand(normalized) {
		return false, ""
	}
	if promptEvaluationToolOutputHasOnlySuccessFailureCounters(normalized) {
		return false, ""
	}
	if reason := promptEvaluationToolStructuredFailureReason(displayOutput); reason != "" {
		return true, reason
	}
	return false, ""
}

func promptEvaluationToolOutputText(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || !strings.HasPrefix(trimmed, "[") {
		return output
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parts); err != nil || len(parts) == 0 {
		return output
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(part.Text)
		if text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return output
	}
	return strings.Join(texts, "\n")
}

func promptEvaluationToolOutputIsReadOnlyCommand(output string) bool {
	command := promptEvaluationToolMeaningfulCommand(promptEvaluationToolOutputCommand(output))
	if command == "" {
		return false
	}
	if promptEvaluationToolOutputHasNonEmptyStderr(output) {
		return false
	}
	return strings.HasPrefix(command, "git diff") ||
		strings.HasPrefix(command, "git branch") ||
		strings.HasPrefix(command, "git show") ||
		strings.HasPrefix(command, "git status") ||
		strings.HasPrefix(command, "git log") ||
		strings.HasPrefix(command, "multica issue comment list") ||
		promptEvaluationToolCommandIsReadOnlyShell(command) ||
		promptEvaluationToolCommandReadsLocalArtifact(command)
}

func promptEvaluationToolOutputCommand(output string) string {
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "command:") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "command:"))
	}
	return ""
}

func promptEvaluationToolMeaningfulCommand(command string) string {
	segments := regexp.MustCompile(`\s+(?:&&|\|\|)\s+|;\s*`).Split(command, -1)
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" || strings.HasPrefix(segment, "cd ") || strings.HasPrefix(segment, "export ") {
			continue
		}
		return segment
	}
	return strings.TrimSpace(command)
}

func promptEvaluationToolOutputHasNonEmptyStderr(output string) bool {
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "stderr:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "stderr:"))
		if value != "" && value != "(empty)" {
			return true
		}
	}
	return false
}

func promptEvaluationToolOutputHasToolUseError(output string) bool {
	return strings.Contains(output, "<tool_use_error>")
}

func promptEvaluationToolStructuredFailureReason(output string) string {
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "error:"):
			return "工具结果包含错误信息"
		case strings.HasPrefix(lower, "traceback"):
			return "工具结果包含异常信息"
		case strings.HasPrefix(lower, "runtimeerror:") || strings.HasPrefix(lower, "exception"):
			return "工具结果包含异常信息"
		case strings.HasPrefix(lower, "--- fail:") || strings.HasPrefix(lower, "fail\t") || strings.HasPrefix(lower, "fail "):
			return "工具结果包含失败信息"
		case strings.HasPrefix(lower, "panic:"):
			return "工具结果包含崩溃信息"
		case strings.HasPrefix(lower, "fatal"):
			return "工具结果包含崩溃信息"
		case strings.HasPrefix(lower, "command failed"):
			return "工具结果包含失败信息"
		case regexp.MustCompile(`^make(?:\[\d+\])?: \*\*\* .*\berror\s+\d+\b`).MatchString(lower):
			return "工具结果包含错误信息"
		}
	}
	return ""
}

func promptEvaluationToolCommandIsReadOnlyShell(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	executable := fields[0]
	if slash := strings.LastIndex(executable, "/"); slash >= 0 {
		executable = executable[slash+1:]
	}
	switch executable {
	case "cat", "sed", "nl", "ls", "head", "tail", "rg", "grep", "find":
		return true
	default:
		return false
	}
}

func promptEvaluationToolCommandReadsLocalArtifact(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	executable := fields[0]
	if slash := strings.LastIndex(executable, "/"); slash >= 0 {
		executable = executable[slash+1:]
	}
	if executable != "curl" && executable != "wget" {
		return false
	}
	return strings.Contains(command, "/uploads/") || strings.Contains(command, "/api/attachments/")
}

func promptEvaluationToolOutputHasOnlySuccessFailureCounters(output string) bool {
	for _, fatalNeedle := range []string{"error:", "exception", "panic", "timeout", "timed out", "permission denied", "http 500", "status 500", "错误", "异常", "超时", "无权限"} {
		if strings.Contains(output, fatalNeedle) {
			return false
		}
	}
	successCounters := []*regexp.Regexp{
		regexp.MustCompile(`\b0\s+(?:failed|failure|failures)\b`),
		regexp.MustCompile(`\b0\s+chart\(s\)\s+failed\b`),
		regexp.MustCompile(`\b0\s+test(?:s)?\s+failed\b`),
	}
	for _, pattern := range successCounters {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}

func promptEvaluationToolResultIsContentOnly(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "read", "grep", "glob":
		return true
	default:
		return false
	}
}

func promptEvaluationToolHTTPStatusCode(output string) int {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bhttp(?:/[\d.]+)?\s*(?:status\s*)?([45]\d{2})\b`),
		regexp.MustCompile(`\bstatus(?:\s*code)?\s*[:=]?\s*([45]\d{2})\b`),
	}
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(output)
		if len(matches) < 2 {
			continue
		}
		statusCode, err := strconv.Atoi(matches[1])
		if err == nil {
			return statusCode
		}
	}
	return 0
}

func promptEvaluationToolExitCode(output string) (int, bool) {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bexit\s+(?:status|code)\s*[:=]?\s*(\d+)\b`),
		regexp.MustCompile(`\bexited\s+with\s+(?:status|code)\s*[:=]?\s*(\d+)\b`),
	}
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(output)
		if len(matches) < 2 {
			continue
		}
		exitCode, err := strconv.Atoi(matches[1])
		if err == nil {
			return exitCode, true
		}
	}
	return 0, false
}

func promptEvaluationToolCallID(message protocol.TaskMessagePayload) string {
	for _, key := range []string{"tool_call_id", "call_id", "id", "工具调用ID", "调用ID"} {
		if value := strings.TrimSpace(stringFromAny(message.Input[key])); value != "" {
			return value
		}
	}
	return ""
}

func incrementPromptEvaluationSummary(summary map[string]any, key string) {
	current, _ := summary[key].(int)
	summary[key] = current + 1
}

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func promptEvaluationTraceSpanKind(eventType string) string {
	switch {
	case strings.HasPrefix(eventType, "task."):
		return "生命周期"
	case eventType == "llm.usage_unavailable":
		return "模型用量缺失"
	case strings.HasPrefix(eventType, "llm."):
		return "模型用量"
	default:
		return "trace事件"
	}
}

func promptEvaluationTraceSpanSummary(event TaskTraceEventResponse) string {
	parts := []string{event.EventName}
	if event.FailureReason != "" {
		parts = append(parts, "失败原因："+event.FailureReason)
	}
	if event.Provider != "" || event.Model != "" {
		parts = append(parts, "模型："+strings.Trim(strings.Join([]string{event.Provider, event.Model}, "/"), "/"))
	}
	if tokenTotal := event.InputTokens + event.OutputTokens + event.CacheReadTokens + event.CacheWriteTokens; tokenTotal > 0 {
		parts = append(parts, fmt.Sprintf("token：%d", tokenTotal))
	}
	return truncatePromptEvaluationEvidence(strings.Join(nonEmptyStrings(parts...), "；"), 240)
}

func nonEmptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func promptEvaluationTraceDurationMs(event TaskTraceEventResponse) int64 {
	for _, value := range []*int64{event.TotalMs, event.RunMs, event.DurationMs, event.QueueWaitMs} {
		if value != nil {
			return *value
		}
	}
	return 0
}

func promptEvaluationMessageSpanKind(messageType string) string {
	switch messageType {
	case "tool_use":
		return "工具调用"
	case "tool_result":
		return "工具结果"
	case "thinking":
		return "思考消息"
	case "error":
		return "错误消息"
	default:
		return "Agent消息"
	}
}

func promptEvaluationMessageSpanName(messageType string) string {
	switch messageType {
	case "tool_use":
		return "工具调用"
	case "tool_result":
		return "工具结果"
	case "thinking":
		return "思考过程"
	case "error":
		return "错误输出"
	default:
		return "文本输出"
	}
}

func buildPromptEvaluationEvidenceContext(
	run db.PromptEvaluationRun,
	task *db.AgentTaskQueue,
	refs promptEvaluationEvidenceRefs,
	trials []PromptEvaluationTrialResponse,
	usages []PromptEvaluationTaskUsageResponse,
	messages []protocol.TaskMessagePayload,
	traceEvents []TaskTraceEventResponse,
) map[string]any {
	context := map[string]any{
		"工作区":     uuidToString(run.WorkspaceID),
		"提示词":     uuidToString(run.PromptID),
		"评测资产":    uuidToString(run.AssetID),
		"运行":      uuidToString(run.ID),
		"任务":      uuidToString(run.TaskID),
		"触发来源":    run.TriggerSource,
		"执行Agent": uuidToString(run.AgentID),
		"模型":      run.Model,
		"运行时":     run.RuntimeProvider,
		"运行时标识":   uuidToString(run.RuntimeID),
		"状态":      run.Status,
		"创建者":     uuidToString(run.CreatedBy),
		"开始时间":    timestampToString(run.StartedAt),
		"结束时间":    timestampToString(run.CompletedAt),
		"总耗时ms":   run.TotalDurationMs,
		"输入token": run.InputTokens,
		"输出token": run.OutputTokens,
		"预估成本":    run.EstimatedCost,
		"失败原因":    run.FailureReason,
		"评估结论":    run.Conclusion,
		"证据完整性": map[string]any{
			"用例数":       len(trials),
			"任务用量条数":    len(usages),
			"任务消息条数":    len(messages),
			"trace事件条数": len(traceEvents),
		},
		"输入输出摘要": buildPromptEvaluationIOContext(trials, messages),
	}
	if refs.Prompt != nil {
		context["提示词名称"] = refs.Prompt.Name
		context["提示词类型"] = refs.Prompt.PromptType
		context["提示词版本"] = refs.Prompt.Version
	}
	if refs.Asset != nil {
		context["评测资产名称"] = refs.Asset.Name
		context["评测资产类型"] = refs.Asset.AssetType
	}
	if refs.Agent != nil {
		context["执行Agent名称"] = refs.Agent.Name
		context["执行Agent状态"] = refs.Agent.Status
	}
	if refs.Runtime != nil {
		context["运行时名称"] = refs.Runtime.Name
		context["运行时状态"] = refs.Runtime.Status
		context["运行时提供方"] = refs.Runtime.Provider
	}
	if refs.Issue != nil {
		context["issue标题"] = refs.Issue.Title
		context["issue状态"] = refs.Issue.Status
		context["issue编号"] = refs.Issue.Number
		context["项目"] = uuidToString(refs.Issue.ProjectID)
		context["承接方类型"] = refs.Issue.AssigneeType.String
		context["承接方"] = uuidToString(refs.Issue.AssigneeID)
	}
	if refs.Project != nil {
		context["项目"] = uuidToString(refs.Project.ID)
		context["项目名称"] = refs.Project.Title
		context["项目状态"] = refs.Project.Status
	}
	if refs.Squad != nil {
		context["小队"] = uuidToString(refs.Squad.ID)
		context["小队名称"] = refs.Squad.Name
	}
	if run.ChatSessionID.Valid {
		context["会话"] = uuidToString(run.ChatSessionID)
	}
	if task != nil {
		context["issue"] = uuidToString(task.IssueID)
		context["任务状态"] = task.Status
		context["触发评论"] = uuidToString(task.TriggerCommentID)
		context["自动化运行"] = uuidToString(task.AutopilotRunID)
		context["发起人"] = uuidToString(task.InitiatorUserID)
		context["任务触发摘要"] = task.TriggerSummary.String
		context["任务尝试次数"] = task.Attempt
		context["任务最大尝试次数"] = task.MaxAttempts
		context["是否队长任务"] = task.IsLeaderTask
	}
	return context
}

func buildPromptEvaluationIOContext(trials []PromptEvaluationTrialResponse, messages []protocol.TaskMessagePayload) map[string]any {
	context := map[string]any{
		"用例输入摘要": "未记录",
		"用例输出摘要": "未记录",
		"消息摘要":   "未记录",
	}
	if len(trials) > 0 {
		context["用例输入摘要"] = truncatePromptEvaluationEvidence(promptEvaluationEvidenceSummaryString(trials[0].Input), 300)
		context["用例输出摘要"] = truncatePromptEvaluationEvidence(promptEvaluationEvidenceSummaryString(trials[0].Output), 300)
	}
	if len(messages) > 0 {
		message := messages[len(messages)-1]
		context["消息摘要"] = truncatePromptEvaluationEvidence(firstNonEmptyPromptEvaluationString(message.Content, message.Output, message.Type), 300)
	}
	return context
}

func validPromptEvaluationEvidenceSnapshotType(value string) bool {
	switch value {
	case "手动归档", "验收归档", "自动归档":
		return true
	default:
		return false
	}
}

func buildPromptEvaluationEvidenceSnapshotSummary(evidence PromptEvaluationRunEvidenceResponse, generatedAt time.Time, insight map[string]any) map[string]any {
	run := evidence.Run
	summary := map[string]any{
		"语义版本":                "multica.prompt_evaluation.evidence_snapshot.summary.v1",
		"生成时间":                generatedAt.Format(time.RFC3339),
		"运行ID":                run.ID,
		"运行类型":                run.RunKind,
		"运行状态":                run.Status,
		"触发来源":                run.TriggerSource,
		"总用例数":                run.TotalCases,
		"通过数":                 run.PassedCases,
		"失败数":                 run.FailedCases,
		"通过率":                 run.PassRate,
		"总耗时毫秒":               run.TotalDurationMs,
		"输入token":             run.InputTokens,
		"输出token":             run.OutputTokens,
		"预估成本":                run.EstimatedCost,
		"执行Agent":             run.AgentID,
		"模型":                  run.Model,
		"runtime":             run.RuntimeID,
		"runtime供应商":          run.RuntimeProvider,
		"trace/task id":       run.TaskID,
		"失败原因":                promptEvaluationEvidenceFailureReason(evidence),
		"评估结论":                run.Conclusion,
		"trial数":              len(evidence.Trials),
		"usage行数":             len(evidence.TaskUsage),
		"任务消息数":               len(evidence.TaskMessages),
		"trace事件数":            len(evidence.TraceEvents),
		"execution span数":     len(evidence.ExecutionSpans),
		"tool call chain数":    len(evidence.ToolCallChains),
		"tool call summary行数": len(evidence.ToolCallSummary),
		"上下文字段数":              len(evidence.Context),
	}
	if insight != nil {
		summary["服务端解释"] = map[string]any{
			"质量判断":   stringFromAny(insight["质量判断"]),
			"建议动作":   stringFromAny(insight["建议动作"]),
			"失败主因":   stringFromAny(insight["失败主因"]),
			"维度摘要数":  len(anySliceFromRecord(insight, "维度评分摘要")),
			"维度趋势数":  len(anySliceFromRecord(insight, "维度评分趋势")),
			"优化候选数":  len(anySliceFromRecord(insight, "优化候选证据")),
			"单位通过成本": insight["单位通过成本"],
		}
	}
	return summary
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

func anySliceFromRecord(record map[string]any, key string) []any {
	if record == nil {
		return nil
	}
	switch value := record[key].(type) {
	case []any:
		return value
	case []PromptEvaluationDimensionScoreSummaryResponse:
		result := make([]any, len(value))
		for i := range value {
			result[i] = value[i]
		}
		return result
	case []PromptEvaluationDimensionScoreTrendResponse:
		result := make([]any, len(value))
		for i := range value {
			result[i] = value[i]
		}
		return result
	case []map[string]any:
		result := make([]any, len(value))
		for i := range value {
			result[i] = value[i]
		}
		return result
	default:
		return nil
	}
}

func promptEvaluationEvidenceQualityLabel(run PromptEvaluationRunResponse) string {
	if run.TotalCases == 0 {
		return "暂无用例"
	}
	if run.Status == "失败" || run.FailedCases > 0 {
		return "质量风险高"
	}
	if run.PassRate >= 0.8 {
		return "质量稳定"
	}
	if run.PassRate >= 0.5 {
		return "质量待优化"
	}
	return "质量风险高"
}

func promptEvaluationEvidenceCostPerPassedCase(run PromptEvaluationRunResponse) float64 {
	if run.PassedCases <= 0 {
		return 0
	}
	return run.EstimatedCost / float64(run.PassedCases)
}

func promptEvaluationEvidenceRecommendation(evidence PromptEvaluationRunEvidenceResponse, candidateCount int) string {
	run := evidence.Run
	if run.Status == "失败" || run.FailedCases > 0 {
		if candidateCount > 0 {
			return "已有优化候选，建议验收者优先确认高优先级候选并回归同一数据集版本。"
		}
		return "优先根据失败主因生成优化候选，再用同一实验数据回归验证。"
	}
	if run.PassedCases > 0 && promptEvaluationEvidenceCostPerPassedCase(run) > 0.2 {
		return "质量可用但单位通过成本偏高，建议压缩提示词上下文或尝试更轻模型复测。"
	}
	if run.PassedCases > 0 {
		return "当前运行可作为质量基线，后续观察维度趋势和提示词版本变化。"
	}
	return "先补充可评分运行，形成质量和成本基线。"
}

func promptEvaluationEvidenceFailureReason(evidence PromptEvaluationRunEvidenceResponse) string {
	if strings.TrimSpace(evidence.Run.FailureReason) != "" {
		return evidence.Run.FailureReason
	}
	for i := len(evidence.TraceEvents) - 1; i >= 0; i-- {
		if strings.TrimSpace(evidence.TraceEvents[i].FailureReason) != "" {
			return evidence.TraceEvents[i].FailureReason
		}
	}
	return "无"
}

func promptEvaluationEvidenceSummaryString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

