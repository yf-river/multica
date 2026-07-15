package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/prompteval"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type promptEvaluationOptimizationCandidateReader interface {
	GetPromptEvaluationOptimizationCandidateInWorkspace(context.Context, db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams) (db.PromptEvaluationOptimizationCandidate, error)
}

func loadPromptEvaluationOptimizationCandidate(
	w http.ResponseWriter,
	r *http.Request,
	reader promptEvaluationOptimizationCandidateReader,
	workspaceID pgtype.UUID,
	candidateID pgtype.UUID,
) (db.PromptEvaluationOptimizationCandidate, bool) {
	candidate, err := reader.GetPromptEvaluationOptimizationCandidateInWorkspace(r.Context(), db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams{
		ID:          candidateID,
		WorkspaceID: workspaceID,
	})
	if err == nil {
		return candidate, true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "prompt evaluation optimization candidate not found")
		return db.PromptEvaluationOptimizationCandidate{}, false
	}
	writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation optimization candidate")
	return db.PromptEvaluationOptimizationCandidate{}, false
}

func (h *Handler) ListPromptEvaluationOptimizationCandidates(w http.ResponseWriter, r *http.Request) {
	_, workspaceUUID, ok := h.promptEvaluationWorkspace(w, r)
	if !ok {
		return
	}
	runID, ok := parseOptionalUUIDOrBadRequest(w, r.URL.Query().Get("run_id"), "run_id")
	if !ok {
		return
	}
	promptID, ok := parseOptionalUUIDOrBadRequest(w, r.URL.Query().Get("prompt_id"), "prompt_id")
	if !ok {
		return
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptEvaluationOptimizationCandidateStatus(value) {
			writeError(w, http.StatusBadRequest, "status must be 待确认, 已发布 or 已拒绝")
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}
	limit, ok := parseBoundedInt32OrBadRequest(w, r.URL.Query().Get("limit"), "limit", 50, 1, 200)
	if !ok {
		return
	}
	items, err := h.Queries.ListPromptEvaluationOptimizationCandidates(r.Context(), db.ListPromptEvaluationOptimizationCandidatesParams{
		WorkspaceID: workspaceUUID,
		Limit:       limit,
		RunID:       runID,
		PromptID:    promptID,
		Status:      status,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation optimization candidates")
		return
	}
	resp := make([]PromptEvaluationOptimizationCandidateResponse, len(items))
	for i, item := range items {
		resp[i] = promptEvaluationOptimizationCandidateToResponse(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) CreatePromptEvaluationOptimizationCandidate(w http.ResponseWriter, r *http.Request) {
	_, workspaceUUID, userID, ok := h.requirePromptEvaluationWorkspaceUser(w, r)
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation run id")
	if !ok {
		return
	}
	requestHash, err := hashRequestFingerprint(promptEvaluationCandidateCreateFingerprint{
		RunID: uuidToString(runID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint optimization candidate request")
		return
	}
	idempotencyKey, ok := optionalIdempotencyKey(w, r)
	if !ok {
		return
	}
	requestActorID := parseUUID(userID)
	writeReplayError := resourceCreateReplayErrorWriter(
		"Idempotency-Key was already used with a different optimization candidate request",
		"failed to recover optimization candidate request",
	)
	loadReplay := func() (PromptEvaluationOptimizationCandidateResponse, bool, error) {
		return loadResourceCreateReplay(
			r.Context(), h.Queries, workspaceUUID, requestActorID, resourceTypePromptCandidate,
			idempotencyKey, requestHash,
			func(response PromptEvaluationOptimizationCandidateResponse) bool { return response.ID != "" },
		)
	}
	if replay, found, err := loadReplay(); err != nil {
		writeReplayError(w, err)
		return
	} else if found {
		writeJSON(w, http.StatusCreated, replay)
		return
	}
	run, ok := loadPromptEvaluationRun(w, r, h.Queries, workspaceUUID, runID)
	if !ok {
		return
	}
	if !run.PromptID.Valid {
		writeError(w, http.StatusBadRequest, "prompt_id is required to create an optimization candidate")
		return
	}
	if !promptEvaluationRunHasFailure(run) {
		writeError(w, http.StatusBadRequest, "only failed or not-passed runs can create optimization candidates")
		return
	}
	prompt, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{ID: run.PromptID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeValidationLookupError(w, r, err, "prompt_id does not belong to this workspace", "prompt", "prompt_id", uuidToString(run.PromptID))
		return
	}
	trials, err := h.Queries.ListPromptEvaluationTrialsByRun(r.Context(), db.ListPromptEvaluationTrialsByRunParams{RunID: run.ID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load failed prompt evaluation trials")
		return
	}
	sourceSummary := buildPromptEvaluationCandidateFailureSummary(run, trials)
	runtimeEvidence, err := h.promptEvaluationCandidateRuntimeEvidence(r.Context(), run)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation runtime evidence")
		return
	}
	if runtimeEvidence != nil {
		sourceSummary["真实Agent运行证据"] = runtimeEvidence
		sourceSummary["生成说明"] = "基于结构化运行记录、失败用例和真实智能体 task 证据生成优化候选；候选不会自动替换生产提示词。"
	}
	dimensionSummaries, err := h.Queries.ListPromptEvaluationDimensionScoreSummaries(r.Context(), db.ListPromptEvaluationDimensionScoreSummariesParams{
		WorkspaceID: workspaceUUID,
		AssetID:     run.AssetID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation dimension summaries")
		return
	}
	weakDimensions := promptEvaluationWeakDimensionSummaries(dimensionSummaries)
	if len(weakDimensions) == 0 {
		weakDimensions = promptEvaluationDefaultWeakDimensionSummaries(run)
	}
	candidateMetrics := mustDecodePersistedJSONObject(run.Metrics, "prompt evaluation run metrics")
	if len(weakDimensions) > 0 {
		priority := promptEvaluationCandidatePriority(weakDimensions)
		sourceSummary["失败维度"] = weakDimensions
		sourceSummary["候选优先级"] = priority
		candidateMetrics["失败维度"] = weakDimensions
		candidateMetrics["候选优先级"] = priority
		candidateMetrics["候选优先级依据"] = "基于实验维度评分摘要自动计算，待执行或低分维度优先处理。"
	}
	candidateContent, rationale := buildPromptEvaluationCandidateContent(prompt, run, sourceSummary)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start optimization candidate transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	if !handleResourceCreateReservation(
		w, r.Context(), tx,
		reserveResourceCreateRequest(r.Context(), qtx, workspaceUUID, requestActorID, resourceTypePromptCandidate, idempotencyKey, requestHash),
		loadReplay, writeReplayError,
		"failed to reserve optimization candidate request", http.StatusCreated,
	) {
		return
	}
	item, err := qtx.CreatePromptEvaluationOptimizationCandidateWithID(r.Context(), db.CreatePromptEvaluationOptimizationCandidateWithIDParams{
		ID:                   idempotencyKey,
		WorkspaceID:          workspaceUUID,
		AssetID:              run.AssetID,
		RunID:                run.ID,
		PromptID:             run.PromptID,
		CandidateName:        buildPromptEvaluationCandidateName(prompt, run),
		CandidateContent:     candidateContent,
		FailedCaseCount:      promptEvaluationRunFailedCaseCount(run, trials),
		Rationale:            rationale,
		SourceFailureSummary: mustJSONBytes(sourceSummary),
		SourcePromptSnapshot: mustJSONBytes(buildPromptEvaluationSourcePromptSnapshot(prompt)),
		Metrics:              mustJSONBytes(candidateMetrics),
		Status:               "待确认",
		CreatedBy:            requestActorID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation optimization candidate")
		return
	}
	response := promptEvaluationOptimizationCandidateToResponse(item)
	if err := completeResourceCreateRequest(
		r.Context(), qtx, workspaceUUID, requestActorID, resourceTypePromptCandidate,
		idempotencyKey, requestHash, item.ID, response,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete optimization candidate request")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit optimization candidate")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) promptEvaluationCandidateRuntimeEvidence(ctx context.Context, run db.PromptEvaluationRun) (map[string]any, error) {
	if !run.TaskID.Valid {
		return nil, nil
	}
	usages, err := h.Queries.GetTaskUsage(ctx, run.TaskID)
	if err != nil {
		return nil, err
	}
	messages, err := h.Queries.ListTaskMessages(ctx, run.TaskID)
	if err != nil {
		return nil, err
	}
	traceEvents, err := h.Queries.ListTaskTraceEventsByTask(ctx, run.TaskID)
	if err != nil {
		return nil, err
	}
	usageRows := prompteval.UsageEvidenceRows(usages)
	messageRows := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		messageRows = append(messageRows, map[string]any{
			"seq":     message.Seq,
			"type":    message.Type,
			"tool":    message.Tool.String,
			"content": prompteval.TruncateEvidence(message.Content.String, 800),
			"output":  prompteval.TruncateEvidence(message.Output.String, 800),
		})
	}
	traceRows := make([]map[string]any, 0, len(traceEvents))
	for _, event := range traceEvents {
		traceRows = append(traceRows, map[string]any{
			"event_type":     event.EventType,
			"event_name":     event.EventName,
			"status":         event.Status,
			"provider":       event.Provider,
			"model":          event.Model,
			"input_tokens":   event.InputTokens,
			"output_tokens":  event.OutputTokens,
			"failure_reason": event.FailureReason,
			"error_type":     event.ErrorType,
			"duration_ms":    int64Value(event.DurationMs),
			"queue_wait_ms":  int64Value(event.QueueWaitMs),
			"run_ms":         int64Value(event.RunMs),
			"total_ms":       int64Value(event.TotalMs),
			"metadata":       mustDecodePersistedJSONObject(event.Metadata, "task trace event metadata"),
			"created_at":     timestampToString(event.CreatedAt),
		})
	}
	return map[string]any{
		"task_id":      uuidToString(run.TaskID),
		"chat_session": uuidToPtr(run.ChatSessionID),
		"agent_id":     uuidToPtr(run.AgentID),
		"runtime_id":   uuidToPtr(run.RuntimeID),
		"task用量":       usageRows,
		"task消息":       messageRows,
		"trace事件":      traceRows,
	}, nil
}

func (h *Handler) PublishPromptEvaluationOptimizationCandidate(w http.ResponseWriter, r *http.Request) {
	_, workspaceUUID, userID, ok := h.requirePromptEvaluationWorkspaceUser(w, r)
	if !ok {
		return
	}
	candidateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation optimization candidate id")
	if !ok {
		return
	}
	requestHash, err := hashRequestFingerprint(promptEvaluationCandidatePublishFingerprint{
		CandidateID: uuidToString(candidateID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint optimization candidate publish request")
		return
	}
	idempotencyKey, ok := optionalIdempotencyKey(w, r)
	if !ok {
		return
	}
	requestActorID := parseUUID(userID)
	writeReplayError := resourceCreateReplayErrorWriter(
		"Idempotency-Key was already used with a different optimization candidate publish request",
		"failed to recover optimization candidate publish request",
	)
	loadReplay := func() (PublishPromptEvaluationOptimizationCandidateResponse, bool, error) {
		return loadResourceCreateReplay(
			r.Context(), h.Queries, workspaceUUID, requestActorID, resourceTypePromptPublish,
			idempotencyKey, requestHash,
			func(response PublishPromptEvaluationOptimizationCandidateResponse) bool {
				return response.Candidate.ID != "" && response.Prompt.ID != ""
			},
		)
	}
	if replay, found, err := loadReplay(); err != nil {
		writeReplayError(w, err)
		return
	} else if found {
		writeJSON(w, http.StatusOK, replay)
		return
	}
	candidate, ok := loadPromptEvaluationOptimizationCandidate(w, r, h.Queries, workspaceUUID, candidateID)
	if !ok {
		return
	}
	if candidate.Status != "待确认" {
		writeError(w, http.StatusConflict, "only 待确认 optimization candidates can be published")
		return
	}
	sourcePrompt, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{
		ID:          candidate.PromptID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeValidationLookupError(w, r, err, "source prompt not found in this workspace", "source prompt", "prompt_id", uuidToString(candidate.PromptID))
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start optimization candidate publish transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	if !handleResourceCreateReservation(
		w, r.Context(), tx,
		reserveResourceCreateRequest(r.Context(), qtx, workspaceUUID, requestActorID, resourceTypePromptPublish, idempotencyKey, requestHash),
		loadReplay, writeReplayError,
		"failed to reserve optimization candidate publish request", http.StatusOK,
	) {
		return
	}
	publishedPrompt, err := qtx.CreatePromptLibraryItemVersionWithID(r.Context(), db.CreatePromptLibraryItemVersionWithIDParams{
		ID:          idempotencyKey,
		WorkspaceID: workspaceUUID,
		Name:        buildPromptEvaluationPublishedPromptName(sourcePrompt),
		Description: buildPromptEvaluationPublishedPromptDescription(candidate, sourcePrompt),
		PromptType:  sourcePrompt.PromptType,
		Content:     candidate.CandidateContent,
		Version:     sourcePrompt.Version + 1,
		CreatedBy:   requestActorID,
		ProjectID:   sourcePrompt.ProjectID,
		Variables:   sourcePrompt.Variables,
		Tags:        buildPromptEvaluationPublishedPromptTags(sourcePrompt.Tags),
		Status:      promptLibraryStatusActive,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "published prompt name already exists; create a new optimization candidate and retry")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to publish optimization candidate as prompt")
		return
	}
	if _, err := createPromptLibraryVersion(r.Context(), qtx, publishedPrompt, promptLibraryVersionSourceOptimization, candidate.ID, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record published prompt version")
		return
	}
	updatedCandidate, err := qtx.PublishPromptEvaluationOptimizationCandidate(r.Context(), db.PublishPromptEvaluationOptimizationCandidateParams{
		ID:                candidate.ID,
		WorkspaceID:       workspaceUUID,
		PublishedPromptID: publishedPrompt.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "optimization candidate was already handled")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to mark optimization candidate as published")
		return
	}
	promptResponse, err := promptLibraryItemToResponse(publishedPrompt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prepare published prompt response")
		return
	}
	response := PublishPromptEvaluationOptimizationCandidateResponse{
		Candidate: promptEvaluationOptimizationCandidateToResponse(updatedCandidate),
		Prompt:    promptResponse,
	}
	if err := completeResourceCreateRequest(
		r.Context(), qtx, workspaceUUID, requestActorID, resourceTypePromptPublish,
		idempotencyKey, requestHash, publishedPrompt.ID, response,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete optimization candidate publish request")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit optimization candidate publish")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) UpdatePromptEvaluationOptimizationCandidate(w http.ResponseWriter, r *http.Request) {
	_, workspaceUUID, userID, ok := h.requirePromptEvaluationWorkspaceUser(w, r)
	if !ok {
		return
	}
	candidateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation optimization candidate id")
	if !ok {
		return
	}
	var req UpdatePromptEvaluationOptimizationCandidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.CandidateName)
	content := strings.TrimSpace(req.CandidateContent)
	rationale := strings.TrimSpace(req.Rationale)
	editNote := strings.TrimSpace(req.EditNote)
	if name == "" {
		writeError(w, http.StatusBadRequest, "candidate_name is required")
		return
	}
	if content == "" {
		writeError(w, http.StatusBadRequest, "candidate_content is required")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start optimization candidate update transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, uuidToString(candidateID)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock optimization candidate")
		return
	}
	qtx := h.Queries.WithTx(tx)
	candidate, err := qtx.GetPromptEvaluationOptimizationCandidateInWorkspace(r.Context(), db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams{
		ID: candidateID, WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation optimization candidate not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation optimization candidate")
		return
	}
	if candidate.Status != "待确认" {
		writeError(w, http.StatusConflict, "only 待确认 optimization candidates can be edited")
		return
	}
	var normalizedPatch *PromptEvaluationSkillPatch
	if req.SkillPatch != nil {
		patch, err := normalizePromptEvaluationSkillPatch(*req.SkillPatch, candidate, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		normalizedPatch = &patch
	}
	updatedCandidate, err := qtx.UpdatePromptEvaluationOptimizationCandidateDraft(r.Context(), db.UpdatePromptEvaluationOptimizationCandidateDraftParams{
		ID:               candidateID,
		WorkspaceID:      workspaceUUID,
		CandidateName:    name,
		CandidateContent: content,
		Rationale:        pgtype.Text{String: rationale, Valid: true},
		EditedBy:         parseUUID(userID),
		EditNote:         editNote,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "optimization candidate was already handled")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update optimization candidate")
		return
	}
	if normalizedPatch != nil {
		updatedCandidate, err = mergePromptEvaluationOptimizationCandidateMetricsRow(r.Context(), tx, workspaceUUID, candidateID, map[string]any{
			"skill_patch": *normalizedPatch,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist skill patch candidate")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit optimization candidate update")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationOptimizationCandidateToResponse(updatedCandidate))
}

func (h *Handler) RejectPromptEvaluationOptimizationCandidate(w http.ResponseWriter, r *http.Request) {
	_, workspaceUUID, userID, ok := h.requirePromptEvaluationWorkspaceUser(w, r)
	if !ok {
		return
	}
	candidateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation optimization candidate id")
	if !ok {
		return
	}
	var req RejectPromptEvaluationOptimizationCandidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "验收者人工判定该优化候选暂不采纳。"
	}
	requestHash, err := hashRequestFingerprint(promptEvaluationCandidateRejectFingerprint{
		CandidateID: uuidToString(candidateID),
		Reason:      reason,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint optimization candidate reject request")
		return
	}
	idempotencyKey, ok := optionalIdempotencyKey(w, r)
	if !ok {
		return
	}
	requestActorID := parseUUID(userID)
	writeReplayError := resourceCreateReplayErrorWriter(
		"Idempotency-Key was already used with a different optimization candidate reject request",
		"failed to recover optimization candidate reject request",
	)
	loadReplay := func() (PromptEvaluationOptimizationCandidateResponse, bool, error) {
		return loadResourceCreateReplay(
			r.Context(), h.Queries, workspaceUUID, requestActorID, resourceTypePromptReject,
			idempotencyKey, requestHash,
			func(response PromptEvaluationOptimizationCandidateResponse) bool { return response.ID != "" },
		)
	}
	if replay, found, err := loadReplay(); err != nil {
		writeReplayError(w, err)
		return
	} else if found {
		writeJSON(w, http.StatusOK, replay)
		return
	}
	candidate, ok := loadPromptEvaluationOptimizationCandidate(w, r, h.Queries, workspaceUUID, candidateID)
	if !ok {
		return
	}
	if candidate.Status != "待确认" {
		writeError(w, http.StatusConflict, "only 待确认 optimization candidates can be rejected")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start optimization candidate reject transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	if !handleResourceCreateReservation(
		w, r.Context(), tx,
		reserveResourceCreateRequest(r.Context(), qtx, workspaceUUID, requestActorID, resourceTypePromptReject, idempotencyKey, requestHash),
		loadReplay, writeReplayError,
		"failed to reserve optimization candidate reject request", http.StatusOK,
	) {
		return
	}
	updatedCandidate, err := qtx.RejectPromptEvaluationOptimizationCandidate(r.Context(), db.RejectPromptEvaluationOptimizationCandidateParams{
		ID:          candidateID,
		WorkspaceID: workspaceUUID,
		Reason:      reason,
		HandledBy:   requestActorID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "optimization candidate was already handled")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to reject optimization candidate")
		return
	}
	response := promptEvaluationOptimizationCandidateToResponse(updatedCandidate)
	if err := completeResourceCreateRequest(
		r.Context(), qtx, workspaceUUID, requestActorID, resourceTypePromptReject,
		idempotencyKey, requestHash, candidateID, response,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete optimization candidate reject request")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit optimization candidate rejection")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) SyncPromptEvaluationRunFromTask(w http.ResponseWriter, r *http.Request) {
	_, workspaceUUID, ok := h.promptEvaluationWorkspace(w, r)
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation run id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation run sync transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	run, ok := loadPromptEvaluationRun(w, r, qtx, workspaceUUID, runID)
	if !ok {
		return
	}
	if !run.TaskID.Valid {
		writeError(w, http.StatusBadRequest, "prompt evaluation run is not linked to an agent task")
		return
	}
	updated, err := service.SyncPromptEvaluationRunFromTask(r.Context(), qtx, run)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation run from task")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation run sync")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationRunToResponse(updated))
}

func (h *Handler) syncPromptEvaluationCasesFromPayload(w http.ResponseWriter, r *http.Request, qtx *db.Queries, asset db.PromptEvaluationAsset, createdBy pgtype.UUID) bool {
	existing, err := qtx.ListPromptEvaluationCases(r.Context(), db.ListPromptEvaluationCasesParams{
		WorkspaceID: asset.WorkspaceID,
		AssetID:     asset.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation cases")
		return false
	}
	payloadStartIndex := int32(0)
	for _, item := range existing {
		if item.Source != "payload" && item.CaseIndex >= payloadStartIndex {
			payloadStartIndex = item.CaseIndex + 1
		}
	}
	if err := qtx.DeletePromptEvaluationPayloadCasesByAsset(r.Context(), db.DeletePromptEvaluationPayloadCasesByAssetParams{
		WorkspaceID: asset.WorkspaceID,
		AssetID:     asset.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to refresh prompt evaluation cases")
		return false
	}
	if err := refreshPromptEvaluationDatasetRowCount(r.Context(), qtx, asset.WorkspaceID, asset.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to refresh prompt evaluation dataset rows")
		return false
	}
	if err := refreshPromptEvaluationTestSuiteCaseCount(r.Context(), qtx, asset.WorkspaceID, asset.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to refresh prompt evaluation test suite cases")
		return false
	}
	cases := promptEvaluationCases(decodePayloadObject(asset.Payload))
	for idx, item := range cases {
		normalized := normalizePromptEvaluationCase(idx, item)
		expectedContains := mustJSONBytes(normalized.ExpectedContains)
		created, err := qtx.CreatePromptEvaluationCase(r.Context(), db.CreatePromptEvaluationCaseParams{
			WorkspaceID:      asset.WorkspaceID,
			AssetID:          asset.ID,
			PromptID:         asset.PromptID,
			CaseIndex:        payloadStartIndex + int32(idx),
			CaseName:         normalized.Name,
			Variables:        mustJSONBytes(normalized.Variables),
			ExpectedContains: expectedContains,
			Input:            mustJSONBytes(normalized.Input),
			Expected:         mustJSONBytes(normalized.Expected),
			Tags:             mustJSONBytes(normalized.Tags),
			Status:           asset.Status,
			Source:           "payload",
			CreatedBy:        createdBy,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation case")
			return false
		}
		if _, err := syncPromptEvaluationCaseAssertions(r.Context(), qtx, created); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation case assertions")
			return false
		}
		if err := syncPromptEvaluationDatasetRow(r.Context(), qtx, asset, created); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation dataset rows")
			return false
		}
		if err := syncPromptEvaluationTestSuiteCase(r.Context(), qtx, asset, created); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation test suite cases")
			return false
		}
	}
	return true
}
