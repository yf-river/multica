package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	sopStatusFailed = "已失败"

	observabilitySummaryPageSize = 500
)

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

type sopStageMetricResponse struct {
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
		Profile:         mustDecodePersistedJSONObject(run.Profile, "squad SOP run profile"),
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
		Evidence:      mustDecodePersistedJSONObject(event.Evidence, "squad SOP step event evidence"),
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

func squadSOPEventsToResponses(events []db.SquadSopStepEvent) []SquadSOPEventResponse {
	responses := make([]SquadSOPEventResponse, len(events))
	for i, event := range events {
		responses[i] = squadSOPEventToResponse(event)
	}
	return responses
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

func (h *Handler) buildSOPStageMetrics(ctx context.Context, profile []byte, events []SquadSOPEventResponse) ([]sopStageMetricResponse, error) {
	type stageAccumulator struct {
		metric sopStageMetricResponse
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
			metric: sopStageMetricResponse{
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
	out := make([]sopStageMetricResponse, 0, len(accs))
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
		eventResp := squadSOPEventsToResponses(events)
		runResp, err := h.squadSOPRunToResponseWithStageMetrics(r.Context(), run, eventResp)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to build SOP run metrics")
			return
		}
		resp = append(resp, runResp)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
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

func (h *Handler) GetWorkspaceObservabilitySummary(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace_id")
	if !ok {
		return
	}
	since, ok := parseRFC3339OrBadRequest(w, r.URL.Query().Get("since"), "since")
	if !ok {
		return
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
	value := mustDecodePersistedJSONObject(raw, "squad SOP step event evidence")
	if len(value) == 0 {
		return 0
	}
	return 1
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
			"SOP 执行数":   len(runs),
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
) []sopStageMetricResponse {
	type stageAccumulator struct {
		metric sopStageMetricResponse
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
			metric: sopStageMetricResponse{
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
	result := make([]sopStageMetricResponse, 0, len(accs))
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
