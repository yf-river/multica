package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	sopStatusPending   = "待开始"
	sopStatusRunning   = "进行中"
	sopStatusCompleted = "已完成"
	sopStatusFailed    = "已失败"
	sopStatusBlocked   = "已阻塞"

	observabilitySummaryPageSize = 500
)

var validSOPStatuses = map[string]bool{
	sopStatusPending:   true,
	sopStatusRunning:   true,
	sopStatusCompleted: true,
	sopStatusFailed:    true,
	sopStatusBlocked:   true,
}

var validSOPEventTypes = map[string]bool{
	"步骤开始": true,
	"步骤完成": true,
	"步骤失败": true,
	"追加证据": true,
	"人工确认": true,
	"测试结果": true,
	"优化运行": true,
}

type SquadSOPRunResponse struct {
	ID              string                  `json:"id"`
	WorkspaceID     string                  `json:"workspace_id"`
	IssueID         string                  `json:"issue_id"`
	SquadID         string                  `json:"squad_id"`
	LeaderTaskID    *string                 `json:"leader_task_id"`
	ProfileKey      string                  `json:"profile_key"`
	Profile         any                     `json:"profile"`
	Status          string                  `json:"status"`
	CurrentStepKey  string                  `json:"current_step_key"`
	StartedAt       string                  `json:"started_at"`
	CompletedAt     *string                 `json:"completed_at"`
	TotalDurationMs *int64                  `json:"total_duration_ms"`
	Metrics         map[string]any          `json:"metrics"`
	Events          []SquadSOPEventResponse `json:"events,omitempty"`
	CreatedAt       string                  `json:"created_at"`
	UpdatedAt       string                  `json:"updated_at"`
}

type SquadSOPEventResponse struct {
	ID            string         `json:"id"`
	RunID         string         `json:"run_id"`
	WorkspaceID   string         `json:"workspace_id"`
	IssueID       string         `json:"issue_id"`
	SquadID       string         `json:"squad_id"`
	StepKey       string         `json:"step_key"`
	StepName      string         `json:"step_name"`
	RoleKey       string         `json:"role_key"`
	EventType     string         `json:"event_type"`
	Status        string         `json:"status"`
	Evidence      any            `json:"evidence"`
	Reason        string         `json:"reason"`
	DurationMs    *int64         `json:"duration_ms"`
	CreatedByType string         `json:"created_by_type"`
	CreatedByID   *string        `json:"created_by_id"`
	TaskID        *string        `json:"task_id"`
	CreatedAt     string         `json:"created_at"`
	Metrics       map[string]any `json:"metrics"`
}

type SOPStageMetricResponse struct {
	StepKey          string `json:"step_key"`
	StepName         string `json:"step_name"`
	RoleKey          string `json:"role_key"`
	Status           string `json:"status"`
	DurationMs       int64  `json:"duration_ms"`
	EventCount       int    `json:"event_count"`
	EvidenceCount    int    `json:"evidence_count"`
	TaskCount        int    `json:"task_count"`
	MessageCount     int    `json:"message_count"`
	AgentTurnCount   int    `json:"agent_turn_count"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

type CreateSOPRunRequest struct {
	Status         string          `json:"status"`
	CurrentStepKey string          `json:"current_step_key"`
	Profile        json.RawMessage `json:"profile"`
}

type CreateSOPStepEventRequest struct {
	EventType     string          `json:"event_type"`
	Status        string          `json:"status"`
	StepName      string          `json:"step_name"`
	RoleKey       string          `json:"role_key"`
	Evidence      json.RawMessage `json:"evidence"`
	Reason        string          `json:"reason"`
	DurationMs    *int64          `json:"duration_ms"`
	CreatedByType string          `json:"created_by_type"`
	CreatedByID   string          `json:"created_by_id"`
	TaskID        string          `json:"task_id"`
	UpdateRun     *bool           `json:"update_run"`
}

type sopProfileStep struct {
	Key     string
	Name    string
	RoleKey string
}

func squadSOPRunToResponse(run db.SquadSopRun, events []SquadSOPEventResponse) SquadSOPRunResponse {
	metrics := map[string]any{
		"总耗时":    int64Value(run.TotalDurationMs),
		"阶段数":    countDistinctSteps(events),
		"证据数":    countEvidence(events),
		"失败原因":   firstFailureReason(events),
		"当前阶段":   run.CurrentStepKey,
		"小队执行状态": run.Status,
	}
	return SquadSOPRunResponse{
		ID:              uuidToString(run.ID),
		WorkspaceID:     uuidToString(run.WorkspaceID),
		IssueID:         uuidToString(run.IssueID),
		SquadID:         uuidToString(run.SquadID),
		LeaderTaskID:    uuidToPtr(run.LeaderTaskID),
		ProfileKey:      run.ProfileKey,
		Profile:         decodeJSONDefault(run.Profile, map[string]any{}),
		Status:          run.Status,
		CurrentStepKey:  run.CurrentStepKey,
		StartedAt:       timestampToString(run.StartedAt),
		CompletedAt:     timestampToPtr(run.CompletedAt),
		TotalDurationMs: int64Ptr(run.TotalDurationMs),
		Metrics:         metrics,
		Events:          events,
		CreatedAt:       timestampToString(run.CreatedAt),
		UpdatedAt:       timestampToString(run.UpdatedAt),
	}
}

func squadSOPEventToResponse(event db.SquadSopStepEvent) SquadSOPEventResponse {
	duration := int64Ptr(event.DurationMs)
	return SquadSOPEventResponse{
		ID:            uuidToString(event.ID),
		RunID:         uuidToString(event.RunID),
		WorkspaceID:   uuidToString(event.WorkspaceID),
		IssueID:       uuidToString(event.IssueID),
		SquadID:       uuidToString(event.SquadID),
		StepKey:       event.StepKey,
		StepName:      event.StepName,
		RoleKey:       event.RoleKey,
		EventType:     event.EventType,
		Status:        event.Status,
		Evidence:      decodeJSONDefault(event.Evidence, map[string]any{}),
		Reason:        event.Reason,
		DurationMs:    duration,
		CreatedByType: event.CreatedByType,
		CreatedByID:   uuidToPtr(event.CreatedByID),
		TaskID:        uuidToPtr(event.TaskID),
		CreatedAt:     timestampToString(event.CreatedAt),
		Metrics: map[string]any{
			"阶段耗时": duration,
			"失败原因": event.Reason,
			"证据数":  evidenceCount(event.Evidence),
		},
	}
}

func (h *Handler) squadSOPRunToResponseWithStageMetrics(ctx context.Context, run db.SquadSopRun, events []SquadSOPEventResponse) (SquadSOPRunResponse, error) {
	resp := squadSOPRunToResponse(run, events)
	stageMetrics, err := h.buildSOPStageMetrics(ctx, run.Profile, events)
	if err != nil {
		return SquadSOPRunResponse{}, err
	}
	resp.Metrics["阶段指标"] = stageMetrics
	return resp, nil
}

func (h *Handler) buildSOPStageMetrics(ctx context.Context, profile []byte, events []SquadSOPEventResponse) ([]SOPStageMetricResponse, error) {
	type stageAccumulator struct {
		metric SOPStageMetricResponse
		tasks  map[string]struct{}
	}
	steps := sopProfileStepsForHandler(profile)
	accs := make([]*stageAccumulator, 0, len(steps))
	byKey := map[string]*stageAccumulator{}
	ensure := func(stepKey, stepName, roleKey string) *stageAccumulator {
		if acc, ok := byKey[stepKey]; ok {
			if acc.metric.StepName == "" {
				acc.metric.StepName = stepName
			}
			if acc.metric.RoleKey == "" {
				acc.metric.RoleKey = roleKey
			}
			return acc
		}
		acc := &stageAccumulator{
			metric: SOPStageMetricResponse{
				StepKey:  stepKey,
				StepName: stepName,
				RoleKey:  roleKey,
				Status:   "未开始",
			},
			tasks: map[string]struct{}{},
		}
		accs = append(accs, acc)
		byKey[stepKey] = acc
		return acc
	}
	for _, step := range steps {
		ensure(step.Key, step.Name, step.RoleKey)
	}
	for _, event := range events {
		stepKey := event.StepKey
		if strings.TrimSpace(stepKey) == "" {
			stepKey = "unknown"
		}
		acc := ensure(stepKey, event.StepName, event.RoleKey)
		acc.metric.EventCount++
		acc.metric.EvidenceCount += evidenceCountFromAny(event.Evidence)
		if event.DurationMs != nil {
			acc.metric.DurationMs += *event.DurationMs
		}
		if strings.TrimSpace(event.Status) != "" {
			acc.metric.Status = event.Status
		}
		if event.TaskID == nil || strings.TrimSpace(*event.TaskID) == "" {
			continue
		}
		taskID := strings.TrimSpace(*event.TaskID)
		if _, seen := acc.tasks[taskID]; seen {
			continue
		}
		acc.tasks[taskID] = struct{}{}
		acc.metric.TaskCount++
		taskUUID := parseUUID(taskID)
		usages, err := h.Queries.GetTaskUsage(ctx, taskUUID)
		if err != nil {
			return nil, err
		}
		for _, usage := range usages {
			acc.metric.InputTokens += usage.InputTokens
			acc.metric.OutputTokens += usage.OutputTokens
			acc.metric.CacheReadTokens += usage.CacheReadTokens
			acc.metric.CacheWriteTokens += usage.CacheWriteTokens
		}
		messages, err := h.Queries.ListTaskMessages(ctx, taskUUID)
		if err != nil {
			return nil, err
		}
		acc.metric.MessageCount += len(messages)
		for _, message := range messages {
			if isAgentTurnMessageType(message.Type) {
				acc.metric.AgentTurnCount++
			}
		}
	}
	out := make([]SOPStageMetricResponse, 0, len(accs))
	for _, acc := range accs {
		out = append(out, acc.metric)
	}
	return out, nil
}

func evidenceCountFromAny(v any) int {
	switch evidence := v.(type) {
	case map[string]any:
		return len(evidence)
	default:
		return 0
	}
}

func isAgentTurnMessageType(messageType string) bool {
	switch strings.ToLower(strings.TrimSpace(messageType)) {
	case "text", "thinking", "assistant", "agent_message", "message":
		return true
	default:
		return false
	}
}

func (h *Handler) ListIssueSOPRuns(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadSOPIssue(w, r)
	if !ok {
		return
	}
	runs, err := h.Queries.ListIssueSquadSOPRuns(r.Context(), db.ListIssueSquadSOPRunsParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list SOP runs")
		return
	}
	resp := make([]SquadSOPRunResponse, 0, len(runs))
	for _, run := range runs {
		events, err := h.Queries.ListSquadSOPStepEventsByRun(r.Context(), run.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list SOP events")
			return
		}
		eventResp := make([]SquadSOPEventResponse, 0, len(events))
		for _, event := range events {
			eventResp = append(eventResp, squadSOPEventToResponse(event))
		}
		runResp, err := h.squadSOPRunToResponseWithStageMetrics(r.Context(), run, eventResp)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to build SOP run metrics")
			return
		}
		resp = append(resp, runResp)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) CreateIssueSOPRun(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadSOPIssue(w, r)
	if !ok {
		return
	}
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		writeError(w, http.StatusBadRequest, "issue must be assigned to a squad")
		return
	}
	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "assignee squad does not belong to this workspace")
		return
	}
	var req CreateSOPRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	status := req.Status
	if status == "" {
		status = sopStatusRunning
	}
	if !validSOPStatuses[status] {
		writeError(w, http.StatusBadRequest, "status must be 待开始, 进行中, 已完成, 已失败 or 已阻塞")
		return
	}
	profile := normalizeSOPProfileForHandler(squad.SopProfile, req.Profile)
	profileKey, currentStepKey, _, _ := sopProfileSummaryForHandler(profile)
	if req.CurrentStepKey != "" {
		currentStepKey = strings.TrimSpace(req.CurrentStepKey)
	}
	run, err := h.Queries.CreateSquadSOPRun(r.Context(), db.CreateSquadSOPRunParams{
		WorkspaceID:    issue.WorkspaceID,
		IssueID:        issue.ID,
		SquadID:        squad.ID,
		ProfileKey:     profileKey,
		Profile:        profile,
		Status:         status,
		CurrentStepKey: currentStepKey,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create SOP run")
		return
	}
	writeJSON(w, http.StatusCreated, squadSOPRunToResponse(run, nil))
}

func (h *Handler) RecordSOPStepEvent(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "runId"), "run_id")
	if !ok {
		return
	}
	stepKey := strings.TrimSpace(chi.URLParam(r, "stepId"))
	if stepKey == "" {
		writeError(w, http.StatusBadRequest, "step_id is required")
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	run, err := h.Queries.GetSquadSOPRunInWorkspace(r.Context(), db.GetSquadSOPRunInWorkspaceParams{
		ID:          runID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "SOP run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load SOP run")
		return
	}
	var req CreateSOPStepEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.EventType == "" {
		req.EventType = "追加证据"
	}
	if !validSOPEventTypes[req.EventType] {
		writeError(w, http.StatusBadRequest, "event_type must be 步骤开始, 步骤完成, 步骤失败, 追加证据, 人工确认, 测试结果 or 优化运行")
		return
	}
	if req.Status != "" && !validSOPStatuses[req.Status] {
		writeError(w, http.StatusBadRequest, "status must be 待开始, 进行中, 已完成, 已失败 or 已阻塞")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start SOP event transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	run, err = qtx.LockSquadSOPRunInWorkspace(r.Context(), db.LockSquadSOPRunInWorkspaceParams{
		ID:          runID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "SOP run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock SOP run")
		return
	}
	profileSteps := sopProfileStepsForHandler(run.Profile)
	step, stepIndex, stepKnown := findSOPProfileStep(profileSteps, stepKey)
	if len(profileSteps) > 0 && !stepKnown {
		writeError(w, http.StatusBadRequest, "step_id 必须存在于 SOP profile 的 steps 中")
		return
	}
	if stepKnown {
		if strings.TrimSpace(req.RoleKey) != "" && step.RoleKey != "" && strings.TrimSpace(req.RoleKey) != step.RoleKey {
			writeError(w, http.StatusBadRequest, "role_key 必须匹配 SOP profile 中该阶段的角色")
			return
		}
		if strings.TrimSpace(req.StepName) == "" {
			req.StepName = step.Name
		}
		if strings.TrimSpace(req.RoleKey) == "" {
			req.RoleKey = step.RoleKey
		}
	}
	if strings.TrimSpace(req.Status) == "" {
		req.Status = defaultSOPEventStatus(req.EventType, stepIndex, len(profileSteps))
	}
	updateRun := shouldUpdateSOPRun(req.EventType, req.UpdateRun)
	if updateRun && stepKnown && strings.TrimSpace(run.CurrentStepKey) != "" {
		_, currentIndex, currentKnown := findSOPProfileStep(profileSteps, run.CurrentStepKey)
		if currentKnown && stepIndex > currentIndex {
			writeError(w, http.StatusBadRequest, "不能跳过当前 SOP 阶段")
			return
		}
		if currentKnown && stepIndex < currentIndex {
			writeError(w, http.StatusBadRequest, "不能回退到已完成的 SOP 阶段")
			return
		}
	}
	nextStatus, nextStepKey, shouldUpdate := "", "", false
	if updateRun {
		nextStatus, nextStepKey, shouldUpdate = nextSOPRunState(run, profileSteps, stepIndex, stepKey, req)
		if shouldUpdate && isTerminalSOPStatus(run.Status) && !isTerminalSOPStatus(nextStatus) {
			writeError(w, http.StatusBadRequest, "已结束的 SOP run 不能回退为非终态")
			return
		}
	}
	evidence, ok := jsonObjectField(w, req.Evidence, "evidence")
	if !ok {
		return
	}
	var duration pgtype.Int8
	if req.DurationMs != nil {
		duration = pgtype.Int8{Int64: *req.DurationMs, Valid: true}
	}
	createdByType, createdByID := h.sopEventActor(r)
	taskID, ok := optionalUUIDParam(w, req.TaskID, "task_id")
	if !ok {
		return
	}
	event, err := qtx.CreateSquadSOPStepEvent(r.Context(), db.CreateSquadSOPStepEventParams{
		RunID:         run.ID,
		WorkspaceID:   run.WorkspaceID,
		IssueID:       run.IssueID,
		SquadID:       run.SquadID,
		StepKey:       stepKey,
		StepName:      req.StepName,
		RoleKey:       req.RoleKey,
		EventType:     req.EventType,
		Status:        req.Status,
		Evidence:      evidence,
		Reason:        req.Reason,
		DurationMs:    duration,
		CreatedByType: createdByType,
		CreatedByID:   createdByID,
		TaskID:        taskID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record SOP event")
		return
	}
	if shouldUpdate {
		if _, err := qtx.UpdateSquadSOPRunStatus(r.Context(), db.UpdateSquadSOPRunStatusParams{
			ID:             run.ID,
			WorkspaceID:    run.WorkspaceID,
			Status:         nextStatus,
			CurrentStepKey: pgtype.Text{String: nextStepKey, Valid: true},
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update SOP run state")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit SOP event")
		return
	}
	writeJSON(w, http.StatusCreated, squadSOPEventToResponse(event))
}

func sopProfileStepsForHandler(profile []byte) []sopProfileStep {
	var obj map[string]any
	if json.Unmarshal(profile, &obj) != nil || obj == nil {
		return nil
	}
	rawSteps, _ := obj["steps"].([]any)
	steps := make([]sopProfileStep, 0, len(rawSteps))
	for _, raw := range rawSteps {
		step, _ := raw.(map[string]any)
		if step == nil {
			continue
		}
		key := firstStringField(step, "key", "step_key", "id")
		if key == "" {
			continue
		}
		steps = append(steps, sopProfileStep{
			Key:     key,
			Name:    firstStringField(step, "name", "title", "label"),
			RoleKey: firstStringField(step, "role_key", "role"),
		})
	}
	return steps
}

func findSOPProfileStep(steps []sopProfileStep, key string) (sopProfileStep, int, bool) {
	for index, step := range steps {
		if step.Key == key {
			return step, index, true
		}
	}
	return sopProfileStep{}, -1, false
}

func defaultSOPEventStatus(eventType string, stepIndex int, stepCount int) string {
	switch eventType {
	case "步骤失败":
		return sopStatusFailed
	case "步骤完成":
		if stepCount > 0 && stepIndex == stepCount-1 {
			return sopStatusCompleted
		}
		return sopStatusCompleted
	case "步骤开始":
		return sopStatusRunning
	default:
		return sopStatusRunning
	}
}

func shouldUpdateSOPRun(eventType string, updateRun *bool) bool {
	if updateRun != nil {
		return *updateRun
	}
	return eventType == "步骤开始" || eventType == "步骤完成" || eventType == "步骤失败"
}

func isTerminalSOPStatus(status string) bool {
	return status == sopStatusCompleted || status == sopStatusFailed
}

func nextSOPRunState(run db.SquadSopRun, steps []sopProfileStep, stepIndex int, stepKey string, req CreateSOPStepEventRequest) (status, currentStepKey string, ok bool) {
	switch req.EventType {
	case "步骤完成":
		if len(steps) > 0 && stepIndex >= 0 && stepIndex < len(steps)-1 {
			return sopStatusRunning, steps[stepIndex+1].Key, true
		}
		return sopStatusCompleted, stepKey, true
	case "步骤失败":
		return sopStatusFailed, stepKey, true
	case "步骤开始":
		return sopStatusRunning, stepKey, true
	default:
		if req.Status == "" {
			return "", "", false
		}
		nextStep := stepKey
		if req.Status == sopStatusCompleted && len(steps) > 0 && stepIndex >= 0 && stepIndex < len(steps)-1 {
			nextStep = steps[stepIndex+1].Key
			return sopStatusRunning, nextStep, true
		}
		if req.Status == sopStatusCompleted && len(steps) > 0 && stepIndex >= 0 && stepIndex == len(steps)-1 {
			return sopStatusCompleted, stepKey, true
		}
		if nextStep == "" {
			nextStep = run.CurrentStepKey
		}
		return req.Status, nextStep, true
	}
}

func (h *Handler) GetWorkspaceObservabilitySummary(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace_id")
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
	squadID, ok := optionalUUIDParam(w, r.URL.Query().Get("squad_id"), "squad_id")
	if !ok {
		return
	}
	projectID, ok := optionalUUIDParam(w, r.URL.Query().Get("project_id"), "project_id")
	if !ok {
		return
	}
	agentID, ok := optionalUUIDParam(w, r.URL.Query().Get("agent_id"), "agent_id")
	if !ok {
		return
	}
	runs, err := h.listAllWorkspaceSquadSOPRuns(r.Context(), workspaceID, since, squadID, projectID, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list SOP runs")
		return
	}
	traces, err := h.listAllWorkspaceTaskTraceEvents(r.Context(), workspaceID, since, squadID, projectID, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list task trace events")
		return
	}
	events, err := h.listAllWorkspaceSquadSOPStepEvents(r.Context(), workspaceID, since, squadID, projectID, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list SOP step events")
		return
	}
	taskMessages, err := h.loadObservabilityTaskMessages(r.Context(), events)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list task messages")
		return
	}
	summary := buildObservabilitySummary(runs, events, traces, taskMessages, int64(len(events)), 0)
	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) listAllWorkspaceSquadSOPRuns(ctx context.Context, workspaceID pgtype.UUID, since pgtype.Timestamptz, squadID, projectID, agentID pgtype.UUID) ([]db.SquadSopRun, error) {
	var out []db.SquadSopRun
	for offset := int32(0); ; offset += observabilitySummaryPageSize {
		items, err := h.Queries.ListWorkspaceSquadSOPRuns(ctx, db.ListWorkspaceSquadSOPRunsParams{
			WorkspaceID: workspaceID,
			Limit:       observabilitySummaryPageSize,
			Offset:      offset,
			Since:       since,
			SquadID:     squadID,
			ProjectID:   projectID,
			AgentID:     agentID,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(items) < observabilitySummaryPageSize {
			return out, nil
		}
	}
}

func (h *Handler) listAllWorkspaceTaskTraceEvents(ctx context.Context, workspaceID pgtype.UUID, since pgtype.Timestamptz, squadID, projectID, agentID pgtype.UUID) ([]db.TaskTraceEvent, error) {
	var out []db.TaskTraceEvent
	for offset := int32(0); ; offset += observabilitySummaryPageSize {
		items, err := h.Queries.ListWorkspaceTaskTraceEvents(ctx, db.ListWorkspaceTaskTraceEventsParams{
			WorkspaceID: workspaceID,
			Limit:       observabilitySummaryPageSize,
			Offset:      offset,
			Since:       since,
			SquadID:     squadID,
			ProjectID:   projectID,
			AgentID:     agentID,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(items) < observabilitySummaryPageSize {
			return out, nil
		}
	}
}

func (h *Handler) listAllWorkspaceSquadSOPStepEvents(ctx context.Context, workspaceID pgtype.UUID, since pgtype.Timestamptz, squadID, projectID, agentID pgtype.UUID) ([]db.SquadSopStepEvent, error) {
	var out []db.SquadSopStepEvent
	for offset := int32(0); ; offset += observabilitySummaryPageSize {
		items, err := h.Queries.ListWorkspaceSquadSOPStepEvents(ctx, db.ListWorkspaceSquadSOPStepEventsParams{
			WorkspaceID: workspaceID,
			Limit:       observabilitySummaryPageSize,
			Offset:      offset,
			Since:       since,
			SquadID:     squadID,
			ProjectID:   projectID,
			AgentID:     agentID,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(items) < observabilitySummaryPageSize {
			return out, nil
		}
	}
}

func (h *Handler) loadObservabilityTaskMessages(ctx context.Context, events []db.SquadSopStepEvent) (map[string][]db.TaskMessage, error) {
	result := map[string][]db.TaskMessage{}
	for _, event := range events {
		taskID := uuidToString(event.TaskID)
		if taskID == "" {
			continue
		}
		if _, ok := result[taskID]; ok {
			continue
		}
		messages, err := h.Queries.ListTaskMessages(ctx, event.TaskID)
		if err != nil {
			return nil, err
		}
		result[taskID] = messages
	}
	return result, nil
}

func (h *Handler) loadSOPIssue(w http.ResponseWriter, r *http.Request) (db.Issue, bool) {
	issueID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "issue_id")
	if !ok {
		return db.Issue{}, false
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return db.Issue{}, false
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          issueID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "issue not found")
			return db.Issue{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load issue")
		return db.Issue{}, false
	}
	return issue, true
}

func (h *Handler) sopEventActor(r *http.Request) (string, pgtype.UUID) {
	actorType, actorID := h.resolveActor(r, requestUserID(r), h.resolveWorkspaceID(r))
	return actorType, pgUUIDFromString(actorID)
}

func normalizeSOPProfileForHandler(fallback []byte, override json.RawMessage) []byte {
	if len(override) > 0 && strings.TrimSpace(string(override)) != "" && strings.TrimSpace(string(override)) != "null" {
		var obj map[string]any
		if json.Unmarshal(override, &obj) == nil && obj != nil {
			normalized, err := json.Marshal(obj)
			if err == nil {
				return normalized
			}
		}
	}
	var obj map[string]any
	if json.Unmarshal(fallback, &obj) == nil && obj != nil {
		normalized, err := json.Marshal(obj)
		if err == nil {
			return normalized
		}
	}
	return []byte(`{}`)
}

func sopProfileSummaryForHandler(profile []byte) (profileKey, currentStepKey, currentStepName, roleKey string) {
	profileKey = "custom"
	var obj map[string]any
	if json.Unmarshal(profile, &obj) != nil || obj == nil {
		return profileKey, "", "", ""
	}
	if v, ok := obj["profile_key"].(string); ok && strings.TrimSpace(v) != "" {
		profileKey = strings.TrimSpace(v)
	}
	steps, _ := obj["steps"].([]any)
	if len(steps) == 0 {
		return profileKey, "", "", ""
	}
	step, _ := steps[0].(map[string]any)
	if step == nil {
		return profileKey, "", "", ""
	}
	currentStepKey = firstStringField(step, "key", "step_key", "id")
	currentStepName = firstStringField(step, "name", "title", "label")
	roleKey = firstStringField(step, "role_key", "role")
	return profileKey, currentStepKey, currentStepName, roleKey
}

func firstStringField(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := obj[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func optionalUUIDParam(w http.ResponseWriter, raw string, field string) (pgtype.UUID, bool) {
	if strings.TrimSpace(raw) == "" {
		return pgtype.UUID{}, true
	}
	parsed, ok := parseUUIDOrBadRequest(w, raw, field)
	if !ok {
		return pgtype.UUID{}, false
	}
	return parsed, true
}

func pgUUIDFromString(raw string) pgtype.UUID {
	if strings.TrimSpace(raw) == "" {
		return pgtype.UUID{}
	}
	id, err := util.ParseUUID(raw)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

func int64Value(value pgtype.Int8) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func int64Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}

func countDistinctSteps(events []SquadSOPEventResponse) int {
	steps := map[string]bool{}
	for _, event := range events {
		if event.StepKey != "" {
			steps[event.StepKey] = true
		}
	}
	return len(steps)
}

func countEvidence(events []SquadSOPEventResponse) int {
	total := 0
	for _, event := range events {
		if event.EventType == "追加证据" || event.EventType == "测试结果" || event.EventType == "优化运行" || event.EventType == "人工确认" {
			total++
		}
	}
	return total
}

func firstFailureReason(events []SquadSOPEventResponse) string {
	for _, event := range events {
		if event.Status == sopStatusFailed || event.EventType == "步骤失败" {
			return event.Reason
		}
	}
	return ""
}

func evidenceCount(raw []byte) int {
	value := decodeJSONDefault(raw, map[string]any{})
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case map[string]any:
		if len(typed) == 0 {
			return 0
		}
		return 1
	default:
		return 0
	}
}

type observabilityUsageBreakdown struct {
	Label            string
	Provider         string
	Model            string
	RuntimeID        string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	TaskCount        int
	EstimatedCost    float64
	HasPrice         bool
}

type observabilitySOPStageMetric struct {
	StepKey          string `json:"step_key"`
	StepName         string `json:"step_name"`
	RoleKey          string `json:"role_key"`
	Status           string `json:"status"`
	DurationMs       int64  `json:"duration_ms"`
	EventCount       int    `json:"event_count"`
	EvidenceCount    int    `json:"evidence_count"`
	TaskCount        int    `json:"task_count"`
	MessageCount     int    `json:"message_count"`
	AgentTurnCount   int    `json:"agent_turn_count"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

func buildObservabilitySummary(
	runs []db.SquadSopRun,
	events []db.SquadSopStepEvent,
	traces []db.TaskTraceEvent,
	taskMessages map[string][]db.TaskMessage,
	sopEventCount int64,
	sampleLimit int32,
) map[string]any {
	runMaybeTruncated := false
	traceMaybeTruncated := false
	completenessStatus := "完整"
	completenessReason := "当前筛选条件下的 SOP 执行和任务观测已按全量汇总。"
	statusCounts := map[string]int{}
	squadCounts := map[string]int{}
	issueCounts := map[string]int{}
	var totalDuration int64
	var durationCount int64
	for _, run := range runs {
		statusCounts[run.Status]++
		squadCounts[uuidToString(run.SquadID)]++
		issueCounts[uuidToString(run.IssueID)]++
		if run.TotalDurationMs.Valid {
			totalDuration += run.TotalDurationMs.Int64
			durationCount++
		}
	}
	var queueWait, runMs, totalMs, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64
	var retryCount int64
	var estimatedCost float64
	failureReasons := map[string]int{}
	projectCounts := map[string]int{}
	modelBreakdown := map[string]*observabilityUsageBreakdown{}
	runtimeBreakdown := map[string]*observabilityUsageBreakdown{}
	unpricedModels := map[string]int{}
	usageTracesByTask := map[string][]db.TaskTraceEvent{}
	for _, trace := range traces {
		if trace.QueueWaitMs.Valid {
			queueWait += trace.QueueWaitMs.Int64
		}
		if trace.RunMs.Valid {
			runMs += trace.RunMs.Int64
		}
		if trace.TotalMs.Valid {
			totalMs += trace.TotalMs.Int64
		}
		if trace.Attempt > 1 {
			retryCount += int64(trace.Attempt - 1)
		}
		if trace.FailureReason != "" {
			failureReasons[trace.FailureReason]++
		}
		if trace.ProjectID.Valid {
			projectCounts[uuidToString(trace.ProjectID)]++
		}
		if trace.IssueID.Valid {
			issueCounts[uuidToString(trace.IssueID)]++
		}
	}
	for _, trace := range dedupeObservabilityUsageTraces(traces) {
		if trace.EventType == "llm.usage_reported" {
			taskID := uuidToString(trace.TaskID)
			if taskID != "" {
				usageTracesByTask[taskID] = append(usageTracesByTask[taskID], trace)
			}
		}
		inputTokens += trace.InputTokens
		outputTokens += trace.OutputTokens
		cacheReadTokens += trace.CacheReadTokens
		cacheWriteTokens += trace.CacheWriteTokens
		breakdown, hasPrice := metrics.EstimateUsageCostBreakdownUSD(
			trace.Provider,
			trace.Model,
			trace.InputTokens,
			trace.OutputTokens,
			trace.CacheReadTokens,
			trace.CacheWriteTokens,
		)
		estimatedCost += breakdown.TotalCostUSD
		modelProvider, modelName, priced := metrics.CanonicalModelPriceKey(trace.Model)
		if !priced {
			modelProvider = trace.Provider
			modelName = trace.Model
			unpricedModels[observabilityModelLabel(trace.Provider, trace.Model)]++
		}
		addObservabilityBreakdown(modelBreakdown, observabilityModelLabel(modelProvider, modelName), observabilityUsageBreakdown{
			Label:            observabilityModelLabel(modelProvider, modelName),
			Provider:         modelProvider,
			Model:            modelName,
			InputTokens:      trace.InputTokens,
			OutputTokens:     trace.OutputTokens,
			CacheReadTokens:  trace.CacheReadTokens,
			CacheWriteTokens: trace.CacheWriteTokens,
			TaskCount:        1,
			EstimatedCost:    breakdown.TotalCostUSD,
			HasPrice:         hasPrice,
		})
		runtimeID := ""
		if trace.RuntimeID.Valid {
			runtimeID = uuidToString(trace.RuntimeID)
		}
		addObservabilityBreakdown(runtimeBreakdown, runtimeID, observabilityUsageBreakdown{
			Label:            runtimeID,
			RuntimeID:        runtimeID,
			Provider:         trace.Provider,
			Model:            trace.Model,
			InputTokens:      trace.InputTokens,
			OutputTokens:     trace.OutputTokens,
			CacheReadTokens:  trace.CacheReadTokens,
			CacheWriteTokens: trace.CacheWriteTokens,
			TaskCount:        1,
			EstimatedCost:    breakdown.TotalCostUSD,
			HasPrice:         hasPrice,
		})
	}
	stageBreakdown := buildObservabilitySOPStageBreakdown(runs, events, usageTracesByTask, taskMessages)
	return map[string]any{
		"指标": map[string]any{
			"SOP 执行数":   durationCountOrTotal(durationCount, len(runs)),
			"SOP 事件数":   sopEventCount,
			"阶段耗时":      avgInt64(totalDuration, durationCount),
			"队列等待":      queueWait,
			"执行耗时":      runMs,
			"总耗时":       totalMs,
			"输入 token":  inputTokens,
			"输出 token":  outputTokens,
			"缓存读 token": cacheReadTokens,
			"缓存写 token": cacheWriteTokens,
			"预估成本":      metrics.RoundCostUSD(estimatedCost),
			"失败原因":      sortedReasonCounts(failureReasons),
			"重试次数":      retryCount,
			"证据数":       sopEventCount,
			"缺少模型价格":    sortedReasonCounts(unpricedModels),
			"采样上限":      sampleLimit,
			"SOP 执行样本数": len(runs),
			"任务观测样本数":   len(traces),
			"汇总完整性":     completenessStatus,
		},
		"sop_status_counts":          statusCounts,
		"squad_counts":               squadCounts,
		"project_counts":             projectCounts,
		"issue_counts":               issueCounts,
		"task_trace_total":           len(traces),
		"sop_run_sample_total":       len(runs),
		"task_trace_sample_total":    len(traces),
		"sample_limit":               sampleLimit,
		"sop_run_maybe_truncated":    runMaybeTruncated,
		"task_trace_maybe_truncated": traceMaybeTruncated,
		"summary_completeness": map[string]any{
			"状态":         completenessStatus,
			"说明":         completenessReason,
			"采样上限":       sampleLimit,
			"SOP 执行样本数":  len(runs),
			"任务观测样本数":    len(traces),
			"SOP 执行可能截断": runMaybeTruncated,
			"任务观测可能截断":   traceMaybeTruncated,
		},
		"model_breakdown":     observabilityBreakdownRows(modelBreakdown),
		"runtime_breakdown":   observabilityBreakdownRows(runtimeBreakdown),
		"sop_stage_breakdown": stageBreakdown,
	}
}

func buildObservabilitySOPStageBreakdown(
	runs []db.SquadSopRun,
	events []db.SquadSopStepEvent,
	usageTracesByTask map[string][]db.TaskTraceEvent,
	taskMessages map[string][]db.TaskMessage,
) []observabilitySOPStageMetric {
	type stageAccumulator struct {
		metric observabilitySOPStageMetric
		tasks  map[string]struct{}
	}
	accs := make([]*stageAccumulator, 0)
	byKey := map[string]*stageAccumulator{}
	ensure := func(stepKey, stepName, roleKey string) *stageAccumulator {
		stepKey = strings.TrimSpace(stepKey)
		if stepKey == "" {
			stepKey = "unknown"
		}
		if existing, ok := byKey[stepKey]; ok {
			if existing.metric.StepName == "" {
				existing.metric.StepName = strings.TrimSpace(stepName)
			}
			if existing.metric.RoleKey == "" {
				existing.metric.RoleKey = strings.TrimSpace(roleKey)
			}
			return existing
		}
		acc := &stageAccumulator{
			metric: observabilitySOPStageMetric{
				StepKey:  stepKey,
				StepName: strings.TrimSpace(stepName),
				RoleKey:  strings.TrimSpace(roleKey),
			},
			tasks: map[string]struct{}{},
		}
		byKey[stepKey] = acc
		accs = append(accs, acc)
		return acc
	}
	for _, run := range runs {
		for _, step := range sopProfileStepsForHandler(run.Profile) {
			ensure(step.Key, step.Name, step.RoleKey)
		}
	}
	for _, event := range events {
		acc := ensure(event.StepKey, event.StepName, event.RoleKey)
		acc.metric.EventCount++
		acc.metric.EvidenceCount += evidenceCount(event.Evidence)
		if event.DurationMs.Valid {
			acc.metric.DurationMs += event.DurationMs.Int64
		}
		if strings.TrimSpace(event.Status) != "" {
			acc.metric.Status = event.Status
		}
		taskID := uuidToString(event.TaskID)
		if taskID == "" {
			continue
		}
		if _, seen := acc.tasks[taskID]; seen {
			continue
		}
		acc.tasks[taskID] = struct{}{}
		acc.metric.TaskCount++
		for _, trace := range usageTracesByTask[taskID] {
			acc.metric.InputTokens += trace.InputTokens
			acc.metric.OutputTokens += trace.OutputTokens
			acc.metric.CacheReadTokens += trace.CacheReadTokens
			acc.metric.CacheWriteTokens += trace.CacheWriteTokens
		}
		messages := taskMessages[taskID]
		acc.metric.MessageCount += len(messages)
		for _, message := range messages {
			if isAgentTurnMessageType(message.Type) {
				acc.metric.AgentTurnCount++
			}
		}
	}
	result := make([]observabilitySOPStageMetric, 0, len(accs))
	for _, acc := range accs {
		result = append(result, acc.metric)
	}
	return result
}

func dedupeObservabilityUsageTraces(traces []db.TaskTraceEvent) []db.TaskTraceEvent {
	usageByKey := map[string]db.TaskTraceEvent{}
	usageKeys := []string{}
	result := make([]db.TaskTraceEvent, 0, len(traces))
	for _, trace := range traces {
		if trace.EventType != "llm.usage_reported" {
			result = append(result, trace)
			continue
		}
		key := observabilityUsageTraceKey(trace)
		current, ok := usageByKey[key]
		if !ok {
			usageByKey[key] = trace
			usageKeys = append(usageKeys, key)
			continue
		}
		if trace.CreatedAt.Valid && (!current.CreatedAt.Valid || trace.CreatedAt.Time.After(current.CreatedAt.Time)) {
			usageByKey[key] = trace
		}
	}
	for _, key := range usageKeys {
		result = append(result, usageByKey[key])
	}
	return result
}

func observabilityUsageTraceKey(trace db.TaskTraceEvent) string {
	taskID := uuidToString(trace.TaskID)
	if taskID == "" {
		taskID = uuidToString(trace.ID)
	}
	return strings.Join([]string{
		taskID,
		uuidToString(trace.RuntimeID),
		strings.ToLower(strings.TrimSpace(trace.Provider)),
		strings.TrimSpace(trace.Model),
	}, "|")
}

func addObservabilityBreakdown(items map[string]*observabilityUsageBreakdown, key string, next observabilityUsageBreakdown) {
	if key == "" {
		key = "未记录"
	}
	current, ok := items[key]
	if !ok {
		next.Label = key
		items[key] = &next
		return
	}
	current.InputTokens += next.InputTokens
	current.OutputTokens += next.OutputTokens
	current.CacheReadTokens += next.CacheReadTokens
	current.CacheWriteTokens += next.CacheWriteTokens
	current.TaskCount += next.TaskCount
	current.EstimatedCost = metrics.RoundCostUSD(current.EstimatedCost + next.EstimatedCost)
	current.HasPrice = current.HasPrice || next.HasPrice
	if current.Provider == "" {
		current.Provider = next.Provider
	}
	if current.Model == "" {
		current.Model = next.Model
	}
}

func observabilityBreakdownRows(items map[string]*observabilityUsageBreakdown) []map[string]any {
	rows := make([]*observabilityUsageBreakdown, 0, len(items))
	for _, item := range items {
		rows = append(rows, item)
	}
	sort.Slice(rows, func(i, j int) bool {
		leftTokens := rows[i].InputTokens + rows[i].OutputTokens + rows[i].CacheReadTokens + rows[i].CacheWriteTokens
		rightTokens := rows[j].InputTokens + rows[j].OutputTokens + rows[j].CacheReadTokens + rows[j].CacheWriteTokens
		if rows[i].EstimatedCost != rows[j].EstimatedCost {
			return rows[i].EstimatedCost > rows[j].EstimatedCost
		}
		if leftTokens != rightTokens {
			return leftTokens > rightTokens
		}
		return rows[i].Label < rows[j].Label
	})
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		result = append(result, map[string]any{
			"名称":        row.Label,
			"provider":  row.Provider,
			"model":     row.Model,
			"runtime":   row.RuntimeID,
			"输入 token":  row.InputTokens,
			"输出 token":  row.OutputTokens,
			"缓存读 token": row.CacheReadTokens,
			"缓存写 token": row.CacheWriteTokens,
			"任务数":       row.TaskCount,
			"预估成本":      metrics.RoundCostUSD(row.EstimatedCost),
			"价格已知":      row.HasPrice,
		})
	}
	return result
}

func observabilityModelLabel(provider, model string) string {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" {
		if model == "" {
			return "未记录"
		}
		return model
	}
	if model == "" {
		return provider
	}
	return provider + "/" + model
}

func durationCountOrTotal(_ int64, total int) int {
	return total
}

func avgInt64(total int64, count int64) any {
	if count == 0 {
		return nil
	}
	return total / count
}

func sortedReasonCounts(counts map[string]int) []map[string]any {
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	resp := make([]map[string]any, 0, len(reasons))
	for _, reason := range reasons {
		resp = append(resp, map[string]any{"原因": reason, "次数": counts[reason]})
	}
	return resp
}
