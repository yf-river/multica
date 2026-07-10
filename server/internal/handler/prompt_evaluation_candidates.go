package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) ListPromptEvaluationOptimizationCandidates(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var runID pgtype.UUID
	if value := r.URL.Query().Get("run_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "run_id")
		if !ok {
			return
		}
		runID = parsed
	}
	var promptID pgtype.UUID
	if value := r.URL.Query().Get("prompt_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "prompt_id")
		if !ok {
			return
		}
		promptID = parsed
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptEvaluationOptimizationCandidateStatus(value) {
			writeError(w, http.StatusBadRequest, "status must be 待确认, 已发布 or 已拒绝")
			return
		}
		status = pgtype.Text{String: value, Valid: true}
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
	run, err := h.Queries.GetPromptEvaluationRunInWorkspace(r.Context(), db.GetPromptEvaluationRunInWorkspaceParams{ID: runID, WorkspaceID: workspaceUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation run")
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
		writeError(w, http.StatusBadRequest, "prompt_id does not belong to this workspace")
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
	candidateMetrics, _ := decodeJSONDefault(run.Metrics, map[string]any{}).(map[string]any)
	if candidateMetrics == nil {
		candidateMetrics = map[string]any{}
	}
	if len(weakDimensions) > 0 {
		priority := promptEvaluationCandidatePriority(weakDimensions)
		sourceSummary["失败维度"] = weakDimensions
		sourceSummary["候选优先级"] = priority
		candidateMetrics["失败维度"] = weakDimensions
		candidateMetrics["候选优先级"] = priority
		candidateMetrics["候选优先级依据"] = "基于实验维度评分摘要自动计算，待执行或低分维度优先处理。"
	}
	candidateContent, rationale := buildPromptEvaluationCandidateContent(prompt, run, sourceSummary)
	item, err := h.Queries.CreatePromptEvaluationOptimizationCandidate(r.Context(), db.CreatePromptEvaluationOptimizationCandidateParams{
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
		CreatedBy:            parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation optimization candidate")
		return
	}
	writeJSON(w, http.StatusCreated, promptEvaluationOptimizationCandidateToResponse(item))
}

func (h *Handler) RunPromptEvaluationOptimizationAgent(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "optimization run assets have been removed; create optimization candidates from failed evaluation runs")
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
	usageRows := make([]map[string]any, 0, len(usages))
	for _, usage := range usages {
		breakdown, priced := metrics.EstimateUsageCostBreakdownUSD(
			usage.Provider,
			usage.Model,
			usage.InputTokens,
			usage.OutputTokens,
			usage.CacheReadTokens,
			usage.CacheWriteTokens,
		)
		usageRows = append(usageRows, map[string]any{
			"provider":           usage.Provider,
			"model":              usage.Model,
			"input_tokens":       usage.InputTokens,
			"output_tokens":      usage.OutputTokens,
			"cache_read_tokens":  usage.CacheReadTokens,
			"cache_write_tokens": usage.CacheWriteTokens,
			"estimated_cost":     metrics.RoundCostUSD(breakdown.TotalCostUSD),
			"priced":             priced,
		})
	}
	messageRows := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		messageRows = append(messageRows, map[string]any{
			"seq":     message.Seq,
			"type":    message.Type,
			"tool":    message.Tool.String,
			"content": truncatePromptEvaluationEvidence(message.Content.String, 800),
			"output":  truncatePromptEvaluationEvidence(message.Output.String, 800),
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
			"metadata":       decodeJSONDefault(event.Metadata, map[string]any{}),
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

func (h *Handler) maybeCreatePromptEvaluationCandidateFromOptimizationAgentRun(ctx context.Context, agentRun db.PromptEvaluationRun, createdBy pgtype.UUID) (*db.PromptEvaluationOptimizationCandidate, error) {
	if agentRun.RunKind != "Agent执行" || agentRun.Status == "已入队" || agentRun.Status == "运行中" {
		return nil, nil
	}
	asset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(ctx, db.GetPromptEvaluationAssetInWorkspaceParams{
		ID:          agentRun.AssetID,
		WorkspaceID: agentRun.WorkspaceID,
	})
	if err != nil {
		return nil, err
	}
	payload := decodePayloadObject(asset.Payload)
	taskType := stringFromAny(payload["任务类型"])
	if taskType != "智能体优化运行" && taskType != "Agent 优化运行" {
		return nil, nil
	}
	sourceRunID := strings.TrimSpace(stringFromAny(payload["来源运行"]))
	if sourceRunID == "" {
		return nil, nil
	}
	sourceRunUUID, err := util.ParseUUID(sourceRunID)
	if err != nil {
		return nil, nil
	}
	sourceRun, err := h.Queries.GetPromptEvaluationRunInWorkspace(ctx, db.GetPromptEvaluationRunInWorkspaceParams{
		ID:          sourceRunUUID,
		WorkspaceID: agentRun.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !sourceRun.PromptID.Valid {
		return nil, nil
	}
	existing, err := h.Queries.ListPromptEvaluationOptimizationCandidates(ctx, db.ListPromptEvaluationOptimizationCandidatesParams{
		WorkspaceID: agentRun.WorkspaceID,
		RunID:       sourceRun.ID,
		PromptID:    sourceRun.PromptID,
		Limit:       100,
	})
	if err != nil {
		return nil, err
	}
	for _, item := range existing {
		summary := decodeJSONDefault(item.SourceFailureSummary, map[string]any{})
		if summaryMap, ok := summary.(map[string]any); ok && stringFromAny(summaryMap["来源Agent优化运行"]) == uuidToString(agentRun.ID) {
			return &item, nil
		}
	}
	prompt, err := h.Queries.GetPromptLibraryItemInWorkspace(ctx, db.GetPromptLibraryItemInWorkspaceParams{
		ID:          sourceRun.PromptID,
		WorkspaceID: agentRun.WorkspaceID,
	})
	if err != nil {
		return nil, err
	}
	trials, err := h.Queries.ListPromptEvaluationTrialsByRun(ctx, db.ListPromptEvaluationTrialsByRunParams{
		RunID:       sourceRun.ID,
		WorkspaceID: agentRun.WorkspaceID,
	})
	if err != nil {
		return nil, err
	}
	sourceSummary := buildPromptEvaluationCandidateFailureSummary(sourceRun, trials)
	runtimeEvidence, err := h.promptEvaluationCandidateRuntimeEvidence(ctx, agentRun)
	if err != nil {
		return nil, err
	}
	if runtimeEvidence != nil {
		sourceSummary["真实Agent优化运行证据"] = runtimeEvidence
	}
	sourceSummary["来源Agent优化运行"] = uuidToString(agentRun.ID)
	sourceSummary["来源Agent优化资产"] = uuidToString(agentRun.AssetID)
	sourceSummary["生成说明"] = "由智能体优化运行输出自动生成优化候选；候选不会自动替换生产提示词，必须人工确认后发布。"
	candidateContent, rationale := buildPromptEvaluationAgentOptimizationCandidateContent(prompt, sourceRun, agentRun, sourceSummary)
	item, err := h.Queries.CreatePromptEvaluationOptimizationCandidate(ctx, db.CreatePromptEvaluationOptimizationCandidateParams{
		WorkspaceID:          agentRun.WorkspaceID,
		AssetID:              sourceRun.AssetID,
		RunID:                sourceRun.ID,
		PromptID:             sourceRun.PromptID,
		CandidateName:        buildPromptEvaluationCandidateName(prompt, sourceRun),
		CandidateContent:     candidateContent,
		FailedCaseCount:      promptEvaluationRunFailedCaseCount(sourceRun, trials),
		Rationale:            rationale,
		SourceFailureSummary: mustJSONBytes(sourceSummary),
		SourcePromptSnapshot: mustJSONBytes(buildPromptEvaluationSourcePromptSnapshot(prompt)),
		Metrics:              agentRun.Metrics,
		Status:               "待确认",
		CreatedBy:            createdBy,
	})
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (h *Handler) PublishPromptEvaluationOptimizationCandidate(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	candidateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation optimization candidate id")
	if !ok {
		return
	}
	candidate, err := h.Queries.GetPromptEvaluationOptimizationCandidateInWorkspace(r.Context(), db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams{
		ID:          candidateID,
		WorkspaceID: workspaceUUID,
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
		writeError(w, http.StatusConflict, "only 待确认 optimization candidates can be published")
		return
	}
	sourcePrompt, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{
		ID:          candidate.PromptID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "source prompt not found in this workspace")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start optimization candidate publish transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	publishedPrompt, err := qtx.CreatePromptLibraryItemVersion(r.Context(), db.CreatePromptLibraryItemVersionParams{
		WorkspaceID: workspaceUUID,
		Name:        buildPromptEvaluationPublishedPromptName(sourcePrompt),
		Description: buildPromptEvaluationPublishedPromptDescription(candidate, sourcePrompt),
		PromptType:  sourcePrompt.PromptType,
		Content:     candidate.CandidateContent,
		Version:     sourcePrompt.Version + 1,
		CreatedBy:   parseUUID(userID),
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
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit optimization candidate publish")
		return
	}
	writeJSON(w, http.StatusOK, PublishPromptEvaluationOptimizationCandidateResponse{
		Candidate: promptEvaluationOptimizationCandidateToResponse(updatedCandidate),
		Prompt:    promptLibraryItemToResponse(publishedPrompt),
	})
}

func (h *Handler) UpdatePromptEvaluationOptimizationCandidate(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
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
	candidate, err := h.Queries.GetPromptEvaluationOptimizationCandidateInWorkspace(r.Context(), db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams{
		ID:          candidateID,
		WorkspaceID: workspaceUUID,
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
	updatedCandidate, err := h.Queries.UpdatePromptEvaluationOptimizationCandidateDraft(r.Context(), db.UpdatePromptEvaluationOptimizationCandidateDraftParams{
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
	if req.SkillPatch != nil {
		normalizedPatch, err := normalizePromptEvaluationSkillPatch(*req.SkillPatch, candidate, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		updatedCandidate, err = h.mergePromptEvaluationOptimizationCandidateMetrics(r.Context(), workspaceUUID, candidateID, map[string]any{
			"skill_patch": normalizedPatch,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist skill patch candidate")
			return
		}
	}
	writeJSON(w, http.StatusOK, promptEvaluationOptimizationCandidateToResponse(updatedCandidate))
}

func (h *Handler) RejectPromptEvaluationOptimizationCandidate(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
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
	candidate, err := h.Queries.GetPromptEvaluationOptimizationCandidateInWorkspace(r.Context(), db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams{
		ID:          candidateID,
		WorkspaceID: workspaceUUID,
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
		writeError(w, http.StatusConflict, "only 待确认 optimization candidates can be rejected")
		return
	}
	updatedCandidate, err := h.Queries.RejectPromptEvaluationOptimizationCandidate(r.Context(), db.RejectPromptEvaluationOptimizationCandidateParams{
		ID:          candidateID,
		WorkspaceID: workspaceUUID,
		Reason:      reason,
		HandledBy:   parseUUID(userID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "optimization candidate was already handled")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to reject optimization candidate")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationOptimizationCandidateToResponse(updatedCandidate))
}

func (h *Handler) SyncPromptEvaluationRunFromTask(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation run id")
	if !ok {
		return
	}
	run, err := h.Queries.GetPromptEvaluationRunInWorkspace(r.Context(), db.GetPromptEvaluationRunInWorkspaceParams{
		ID:          runID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation run")
		return
	}
	if !run.TaskID.Valid {
		writeError(w, http.StatusBadRequest, "prompt evaluation run is not linked to an agent task")
		return
	}
	updated, err := service.SyncPromptEvaluationRunFromTask(r.Context(), h.Queries, run)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation run from task")
		return
	}
	createdBy := updated.CreatedBy
	if !createdBy.Valid {
		if userID := requestUserID(r); userID != "" {
			createdBy = parseUUID(userID)
		}
	}
	if _, err := h.maybeCreatePromptEvaluationCandidateFromOptimizationAgentRun(r.Context(), updated, createdBy); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create optimization candidate from agent output")
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
	if err := refreshPromptEvaluationExperimentDimensionCount(r.Context(), qtx, asset.WorkspaceID, asset.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to refresh prompt evaluation experiment dimensions")
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
		if _, err := syncPromptEvaluationCaseAssertions(r.Context(), qtx, created, expectedContains); err != nil {
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

func (h *Handler) exportPromptEvaluationDatasetCases(ctx context.Context, workspaceID pgtype.UUID, assetID pgtype.UUID) ([]db.PromptEvaluationCase, error) {
	const pageSize int32 = 5000
	items := make([]db.PromptEvaluationCase, 0)
	var cursorID pgtype.UUID
	var cursorCaseIndex pgtype.Int4
	for {
		page, err := h.Queries.ListPromptEvaluationCases(ctx, db.ListPromptEvaluationCasesParams{
			WorkspaceID:     workspaceID,
			AssetID:         assetID,
			CursorID:        cursorID,
			CursorCaseIndex: cursorCaseIndex,
			SortBy:          pgtype.Text{String: "case_index", Valid: true},
			SortDirection:   pgtype.Text{String: "asc", Valid: true},
			Limit:           pgtype.Int4{Int32: pageSize, Valid: true},
		})
		if err != nil {
			return nil, err
		}
		items = append(items, page...)
		if int32(len(page)) < pageSize {
			return items, nil
		}
		last := page[len(page)-1]
		cursorID = last.ID
		cursorCaseIndex = pgtype.Int4{Int32: last.CaseIndex, Valid: true}
	}
}

func (h *Handler) ExportPromptEvaluationDataset(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if asset.AssetType != promptEvaluationAssetDataset {
		writeError(w, http.StatusBadRequest, "only 数据集 assets can be exported as dataset protocol")
		return
	}
	items, err := h.exportPromptEvaluationDatasetCases(r.Context(), asset.WorkspaceID, asset.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation dataset cases")
		return
	}
	cases := make([]PromptEvaluationCaseResponse, len(items))
	for i, item := range items {
		cases[i] = promptEvaluationCaseToResponse(item, nil)
	}
	writeJSON(w, http.StatusOK, PromptEvaluationDatasetExportResponse{
		Schema:        promptEvaluationDatasetExportV1,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		SourceAssetID: uuidToString(asset.ID),
		Asset:         promptEvaluationAssetToResponse(asset),
		CaseCount:     len(cases),
		Cases:         cases,
		Payload:       decodePayloadObject(asset.Payload),
	})
}

func normalizeImportedPromptEvaluationCaseSource(source string) string {
	switch strings.TrimSpace(source) {
	case "manual", "trace", "payload":
		return strings.TrimSpace(source)
	default:
		return "payload"
	}
}

func promptEvaluationDatasetImportPayload(export PromptEvaluationDatasetExportResponse, cases []PromptEvaluationCaseResponse) []byte {
	payloadCases := make([]map[string]any, 0, len(cases))
	for _, item := range cases {
		payloadCases = append(payloadCases, map[string]any{
			"case_name":         item.CaseName,
			"case_index":        item.CaseIndex,
			"variables":         item.Variables,
			"expected_contains": item.ExpectedContains,
			"input":             item.Input,
			"expected":          item.Expected,
			"tags":              item.Tags,
			"source":            normalizeImportedPromptEvaluationCaseSource(item.Source),
		})
	}
	return mustJSONBytes(map[string]any{
		"schema": promptEvaluationDatasetImportV1,
		"导入来源": map[string]any{
			"schema":          export.Schema,
			"source_asset_id": export.SourceAssetID,
			"source_name":     export.Asset.Name,
			"exported_at":     export.ExportedAt,
			"imported_at":     time.Now().UTC().Format(time.RFC3339Nano),
		},
		"cases": payloadCases,
	})
}

func (h *Handler) ImportPromptEvaluationDataset(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req ImportPromptEvaluationDatasetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Export.Schema != promptEvaluationDatasetExportV1 {
		writeError(w, http.StatusBadRequest, "export.schema must be "+promptEvaluationDatasetExportV1)
		return
	}
	if req.Export.Asset.AssetType != promptEvaluationAssetDataset {
		writeError(w, http.StatusBadRequest, "only 数据集 exports can be imported")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(req.Export.Asset.Name)
		if name == "" {
			name = "导入数据集"
		}
		name += " 导入副本"
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		description = fmt.Sprintf("从数据集 %s 的完整导出协议导入。", req.Export.SourceAssetID)
	}
	status := normalizePromptLibraryStatus(req.Status)
	if !validPromptLibraryStatus(status) {
		writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
		return
	}
	promptID, ok := h.promptEvaluationPromptID(w, r, workspaceUUID, req.PromptID, pgtype.UUID{})
	if !ok {
		return
	}
	payload := promptEvaluationDatasetImportPayload(req.Export, req.Export.Cases)
	profile := promptEvaluationAssetProfileFromPayload(payload, promptID, promptEvaluationAssetDataset)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation dataset import transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	asset, err := qtx.CreatePromptEvaluationAsset(r.Context(), db.CreatePromptEvaluationAssetParams{
		WorkspaceID:              workspaceUUID,
		Name:                     name,
		Description:              description,
		AssetType:                promptEvaluationAssetDataset,
		CreatedBy:                parseUUID(userID),
		PromptID:                 promptID,
		Payload:                  payload,
		Status:                   status,
		StructureSchema:          profile.StructureSchema,
		StructuredCaseCount:      profile.StructuredCaseCount,
		StructuredVariableCount:  profile.StructuredVariableCount,
		StructuredAssertionCount: profile.StructuredAssertionCount,
		LinkedDatasetCount:       profile.LinkedDatasetCount,
		LinkedPromptCount:        profile.LinkedPromptCount,
		EvaluationDimensionCount: profile.EvaluationDimensionCount,
		ExperimentDimensionCount: profile.ExperimentDimensionCount,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an evaluation dataset with this name already exists")
			return
		}
		if isCheckViolation(err) {
			writeError(w, http.StatusBadRequest, "prompt evaluation dataset import rejected: a field value failed a database constraint")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create imported prompt evaluation dataset")
		return
	}
	importedCases := make([]PromptEvaluationCaseResponse, 0, len(req.Export.Cases))
	for idx, item := range req.Export.Cases {
		caseIndex := item.CaseIndex
		if caseIndex < 0 {
			caseIndex = int32(idx)
		}
		expectedContains := mustJSONBytes(item.ExpectedContains)
		created, err := qtx.CreatePromptEvaluationCase(r.Context(), db.CreatePromptEvaluationCaseParams{
			WorkspaceID:      workspaceUUID,
			AssetID:          asset.ID,
			PromptID:         promptID,
			CaseIndex:        caseIndex,
			CaseName:         strings.TrimSpace(item.CaseName),
			Variables:        mustJSONBytes(item.Variables),
			ExpectedContains: expectedContains,
			Input:            mustJSONBytes(item.Input),
			Expected:         mustJSONBytes(item.Expected),
			Tags:             mustJSONBytes(item.Tags),
			Status:           normalizePromptLibraryStatus(item.Status),
			Source:           normalizeImportedPromptEvaluationCaseSource(item.Source),
			CreatedBy:        parseUUID(userID),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create imported prompt evaluation case")
			return
		}
		assertions, err := syncPromptEvaluationCaseAssertions(r.Context(), qtx, created, expectedContains)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to sync imported prompt evaluation case assertions")
			return
		}
		if err := syncPromptEvaluationDatasetRow(r.Context(), qtx, asset, created); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to sync imported prompt evaluation dataset rows")
			return
		}
		if err := syncPromptEvaluationTestSuiteCase(r.Context(), qtx, asset, created); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to sync imported prompt evaluation test suite cases")
			return
		}
		importedCases = append(importedCases, promptEvaluationCaseToResponse(created, assertions))
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation dataset import")
		return
	}
	createdAsset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{ID: asset.ID, WorkspaceID: asset.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload imported prompt evaluation dataset")
		return
	}
	writeJSON(w, http.StatusCreated, ImportPromptEvaluationDatasetResponse{
		Asset:         promptEvaluationAssetToResponse(createdAsset),
		SourceAssetID: req.Export.SourceAssetID,
		CaseCount:     len(importedCases),
		Cases:         importedCases,
	})
}

