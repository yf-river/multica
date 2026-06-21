package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	promptEvaluationAssetDataset    = "数据集"
	promptEvaluationAssetTestSuite  = "测试套件"
	promptEvaluationAssetExperiment = "实验"
	promptEvaluationAssetOptimize   = "优化运行"
	promptEvaluationAgentName       = "Multica 训练评估 Agent"
	promptEvaluationAgentModel      = "minimax-m2.7-ioa"
)

var promptTemplateVariablePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)

type PromptEvaluationAssetResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	PromptID    *string `json:"prompt_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	AssetType   string  `json:"asset_type"`
	Payload     any     `json:"payload"`
	Status      string  `json:"status"`
	CreatedBy   *string `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

type CreatePromptEvaluationAssetRequest struct {
	PromptID    json.RawMessage `json:"prompt_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	AssetType   string          `json:"asset_type"`
	Payload     json.RawMessage `json:"payload"`
	Status      string          `json:"status"`
}

type UpdatePromptEvaluationAssetRequest struct {
	PromptID    json.RawMessage `json:"prompt_id"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	AssetType   *string         `json:"asset_type"`
	Payload     json.RawMessage `json:"payload"`
	Status      *string         `json:"status"`
}

type promptEvaluationRunResult struct {
	RunAt             string                          `json:"运行时间"`
	AssetType         string                          `json:"资产类型"`
	PromptName        string                          `json:"提示词"`
	TotalCases        int                             `json:"总用例数"`
	PassedCases       int                             `json:"通过用例数"`
	FailedCases       int                             `json:"失败用例数"`
	PassRate          float64                         `json:"通过率"`
	TotalDurationMs   int64                           `json:"总耗时毫秒"`
	AverageDurationMs int64                           `json:"平均耗时毫秒"`
	InputTokens       int                             `json:"输入token"`
	OutputTokens      int                             `json:"输出token"`
	EstimatedCost     float64                         `json:"预估成本"`
	AgentName         string                          `json:"执行Agent"`
	Model             string                          `json:"模型"`
	Runtime           string                          `json:"runtime"`
	TraceTaskID       string                          `json:"trace/task id"`
	FailureReason     string                          `json:"失败原因"`
	Conclusion        string                          `json:"评估结论"`
	MissingVarCount   int                             `json:"缺失变量数"`
	CaseResults       []promptEvaluationCaseRunResult `json:"用例结果"`
}

type promptEvaluationCaseRunResult struct {
	Name             string            `json:"名称"`
	Status           string            `json:"状态"`
	Variables        map[string]string `json:"变量"`
	RenderedPrompt   string            `json:"渲染提示词"`
	UsedVariables    []string          `json:"使用变量"`
	MissingVariables []string          `json:"缺失变量"`
	ExpectedContains []string          `json:"期望包含"`
	MatchedContains  []string          `json:"已匹配"`
}

type normalizedPromptEvaluationCase struct {
	Name             string
	Variables        map[string]string
	ExpectedContains []string
	Input            map[string]any
	Expected         map[string]any
	Tags             []string
}

type PromptEvaluationAgentRunResponse struct {
	Asset         PromptEvaluationAssetResponse `json:"asset"`
	Run           PromptEvaluationRunResponse   `json:"run"`
	TaskID        string                        `json:"task_id"`
	ChatSessionID string                        `json:"chat_session_id"`
	AgentID       string                        `json:"agent_id"`
	RuntimeID     string                        `json:"runtime_id"`
	Model         string                        `json:"model"`
	Status        string                        `json:"status"`
	Message       string                        `json:"message"`
}

type PromptEvaluationRunResponse struct {
	ID                string  `json:"id"`
	WorkspaceID       string  `json:"workspace_id"`
	AssetID           string  `json:"asset_id"`
	PromptID          *string `json:"prompt_id"`
	RunKind           string  `json:"run_kind"`
	Status            string  `json:"status"`
	TriggerSource     string  `json:"trigger_source"`
	AgentID           *string `json:"agent_id"`
	RuntimeID         *string `json:"runtime_id"`
	TaskID            *string `json:"task_id"`
	ChatSessionID     *string `json:"chat_session_id"`
	Model             string  `json:"model"`
	RuntimeProvider   string  `json:"runtime_provider"`
	TotalCases        int32   `json:"total_cases"`
	PassedCases       int32   `json:"passed_cases"`
	FailedCases       int32   `json:"failed_cases"`
	PassRate          float64 `json:"pass_rate"`
	TotalDurationMs   int64   `json:"total_duration_ms"`
	AverageDurationMs int64   `json:"average_duration_ms"`
	InputTokens       int32   `json:"input_tokens"`
	OutputTokens      int32   `json:"output_tokens"`
	EstimatedCost     float64 `json:"estimated_cost"`
	FailureReason     string  `json:"failure_reason"`
	Conclusion        string  `json:"conclusion"`
	Metrics           any     `json:"metrics"`
	Evidence          any     `json:"evidence"`
	StartedAt         string  `json:"started_at"`
	CompletedAt       string  `json:"completed_at"`
	CreatedBy         *string `json:"created_by"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

type PromptEvaluationTrialResponse struct {
	ID             string `json:"id"`
	RunID          string `json:"run_id"`
	WorkspaceID    string `json:"workspace_id"`
	AssetID        string `json:"asset_id"`
	CaseIndex      int32  `json:"case_index"`
	CaseName       string `json:"case_name"`
	Status         string `json:"status"`
	Input          any    `json:"input"`
	Expected       any    `json:"expected"`
	Output         any    `json:"output"`
	RenderedPrompt string `json:"rendered_prompt"`
	InputTokens    int32  `json:"input_tokens"`
	OutputTokens   int32  `json:"output_tokens"`
	DurationMs     int64  `json:"duration_ms"`
	FailureReason  string `json:"failure_reason"`
	Evidence       any    `json:"evidence"`
	CreatedAt      string `json:"created_at"`
}

type PromptEvaluationTaskUsageResponse struct {
	ID               string `json:"id"`
	TaskID           string `json:"task_id"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type PromptEvaluationRunEvidenceResponse struct {
	Run          PromptEvaluationRunResponse         `json:"run"`
	Trials       []PromptEvaluationTrialResponse     `json:"trials"`
	TaskUsage    []PromptEvaluationTaskUsageResponse `json:"task_usage"`
	TaskMessages []protocol.TaskMessagePayload       `json:"task_messages"`
	TraceEvents  []TaskTraceEventResponse            `json:"trace_events"`
	Evidence     any                                 `json:"evidence"`
}

type PromptEvaluationSummaryResponse struct {
	WorkspaceID string           `json:"workspace_id"`
	GeneratedAt string           `json:"generated_at"`
	LastRunAt   string           `json:"last_run_at"`
	Metrics     map[string]any   `json:"指标"`
	Assets      map[string]int64 `json:"资产统计"`
	RunStatus   map[string]int64 `json:"运行状态"`
	Candidates  map[string]int64 `json:"优化候选"`
}

type PromptEvaluationCaseResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	AssetID          string  `json:"asset_id"`
	PromptID         *string `json:"prompt_id"`
	CaseIndex        int32   `json:"case_index"`
	CaseName         string  `json:"case_name"`
	Variables        any     `json:"variables"`
	ExpectedContains any     `json:"expected_contains"`
	Input            any     `json:"input"`
	Expected         any     `json:"expected"`
	Tags             any     `json:"tags"`
	Status           string  `json:"status"`
	Source           string  `json:"source"`
	CreatedBy        *string `json:"created_by"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type PromptEvaluationOptimizationCandidateResponse struct {
	ID                   string  `json:"id"`
	WorkspaceID          string  `json:"workspace_id"`
	AssetID              string  `json:"asset_id"`
	RunID                string  `json:"run_id"`
	PromptID             string  `json:"prompt_id"`
	CandidateName        string  `json:"candidate_name"`
	CandidateContent     string  `json:"candidate_content"`
	Rationale            string  `json:"rationale"`
	FailedCaseCount      int32   `json:"failed_case_count"`
	SourceFailureSummary any     `json:"source_failure_summary"`
	SourcePromptSnapshot any     `json:"source_prompt_snapshot"`
	Metrics              any     `json:"metrics"`
	Status               string  `json:"status"`
	PublishedPromptID    *string `json:"published_prompt_id"`
	PublishedAt          string  `json:"published_at"`
	CreatedBy            *string `json:"created_by"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
}

type PublishPromptEvaluationOptimizationCandidateResponse struct {
	Candidate PromptEvaluationOptimizationCandidateResponse `json:"candidate"`
	Prompt    PromptLibraryItemResponse                     `json:"prompt"`
}

func promptEvaluationAssetToResponse(asset db.PromptEvaluationAsset) PromptEvaluationAssetResponse {
	return PromptEvaluationAssetResponse{
		ID:          uuidToString(asset.ID),
		WorkspaceID: uuidToString(asset.WorkspaceID),
		PromptID:    uuidToPtr(asset.PromptID),
		Name:        asset.Name,
		Description: asset.Description,
		AssetType:   asset.AssetType,
		Payload:     decodeJSONDefault(asset.Payload, map[string]any{}),
		Status:      asset.Status,
		CreatedBy:   uuidToPtr(asset.CreatedBy),
		CreatedAt:   timestampToString(asset.CreatedAt),
		UpdatedAt:   timestampToString(asset.UpdatedAt),
	}
}

func promptEvaluationRunToResponse(run db.PromptEvaluationRun) PromptEvaluationRunResponse {
	return PromptEvaluationRunResponse{
		ID:                uuidToString(run.ID),
		WorkspaceID:       uuidToString(run.WorkspaceID),
		AssetID:           uuidToString(run.AssetID),
		PromptID:          uuidToPtr(run.PromptID),
		RunKind:           run.RunKind,
		Status:            run.Status,
		TriggerSource:     run.TriggerSource,
		AgentID:           uuidToPtr(run.AgentID),
		RuntimeID:         uuidToPtr(run.RuntimeID),
		TaskID:            uuidToPtr(run.TaskID),
		ChatSessionID:     uuidToPtr(run.ChatSessionID),
		Model:             run.Model,
		RuntimeProvider:   run.RuntimeProvider,
		TotalCases:        run.TotalCases,
		PassedCases:       run.PassedCases,
		FailedCases:       run.FailedCases,
		PassRate:          run.PassRate,
		TotalDurationMs:   run.TotalDurationMs,
		AverageDurationMs: run.AverageDurationMs,
		InputTokens:       run.InputTokens,
		OutputTokens:      run.OutputTokens,
		EstimatedCost:     run.EstimatedCost,
		FailureReason:     run.FailureReason,
		Conclusion:        run.Conclusion,
		Metrics:           decodeJSONDefault(run.Metrics, map[string]any{}),
		Evidence:          decodeJSONDefault(run.Evidence, map[string]any{}),
		StartedAt:         timestampToString(run.StartedAt),
		CompletedAt:       timestampToString(run.CompletedAt),
		CreatedBy:         uuidToPtr(run.CreatedBy),
		CreatedAt:         timestampToString(run.CreatedAt),
		UpdatedAt:         timestampToString(run.UpdatedAt),
	}
}

func promptEvaluationTrialToResponse(trial db.PromptEvaluationTrial) PromptEvaluationTrialResponse {
	return PromptEvaluationTrialResponse{
		ID:             uuidToString(trial.ID),
		RunID:          uuidToString(trial.RunID),
		WorkspaceID:    uuidToString(trial.WorkspaceID),
		AssetID:        uuidToString(trial.AssetID),
		CaseIndex:      trial.CaseIndex,
		CaseName:       trial.CaseName,
		Status:         trial.Status,
		Input:          decodeJSONDefault(trial.Input, map[string]any{}),
		Expected:       decodeJSONDefault(trial.Expected, map[string]any{}),
		Output:         decodeJSONDefault(trial.Output, map[string]any{}),
		RenderedPrompt: trial.RenderedPrompt,
		InputTokens:    trial.InputTokens,
		OutputTokens:   trial.OutputTokens,
		DurationMs:     trial.DurationMs,
		FailureReason:  trial.FailureReason,
		Evidence:       decodeJSONDefault(trial.Evidence, map[string]any{}),
		CreatedAt:      timestampToString(trial.CreatedAt),
	}
}

func promptEvaluationTaskUsageToResponse(usage db.TaskUsage) PromptEvaluationTaskUsageResponse {
	return PromptEvaluationTaskUsageResponse{
		ID:               uuidToString(usage.ID),
		TaskID:           uuidToString(usage.TaskID),
		Provider:         usage.Provider,
		Model:            usage.Model,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		CreatedAt:        timestampToString(usage.CreatedAt),
		UpdatedAt:        timestampToString(usage.UpdatedAt),
	}
}

func promptEvaluationCaseToResponse(item db.PromptEvaluationCase) PromptEvaluationCaseResponse {
	return PromptEvaluationCaseResponse{
		ID:               uuidToString(item.ID),
		WorkspaceID:      uuidToString(item.WorkspaceID),
		AssetID:          uuidToString(item.AssetID),
		PromptID:         uuidToPtr(item.PromptID),
		CaseIndex:        item.CaseIndex,
		CaseName:         item.CaseName,
		Variables:        decodeJSONDefault(item.Variables, map[string]any{}),
		ExpectedContains: decodeJSONDefault(item.ExpectedContains, []any{}),
		Input:            decodeJSONDefault(item.Input, map[string]any{}),
		Expected:         decodeJSONDefault(item.Expected, map[string]any{}),
		Tags:             decodeJSONDefault(item.Tags, []any{}),
		Status:           item.Status,
		Source:           item.Source,
		CreatedBy:        uuidToPtr(item.CreatedBy),
		CreatedAt:        timestampToString(item.CreatedAt),
		UpdatedAt:        timestampToString(item.UpdatedAt),
	}
}

func promptEvaluationOptimizationCandidateToResponse(item db.PromptEvaluationOptimizationCandidate) PromptEvaluationOptimizationCandidateResponse {
	return PromptEvaluationOptimizationCandidateResponse{
		ID:                   uuidToString(item.ID),
		WorkspaceID:          uuidToString(item.WorkspaceID),
		AssetID:              uuidToString(item.AssetID),
		RunID:                uuidToString(item.RunID),
		PromptID:             uuidToString(item.PromptID),
		CandidateName:        item.CandidateName,
		CandidateContent:     item.CandidateContent,
		Rationale:            item.Rationale,
		FailedCaseCount:      item.FailedCaseCount,
		SourceFailureSummary: decodeJSONDefault(item.SourceFailureSummary, map[string]any{}),
		SourcePromptSnapshot: decodeJSONDefault(item.SourcePromptSnapshot, map[string]any{}),
		Metrics:              decodeJSONDefault(item.Metrics, map[string]any{}),
		Status:               item.Status,
		PublishedPromptID:    uuidToPtr(item.PublishedPromptID),
		PublishedAt:          timestampToString(item.PublishedAt),
		CreatedBy:            uuidToPtr(item.CreatedBy),
		CreatedAt:            timestampToString(item.CreatedAt),
		UpdatedAt:            timestampToString(item.UpdatedAt),
	}
}

func promptEvaluationSummaryToResponse(workspaceID pgtype.UUID, row db.GetPromptEvaluationSummaryRow) PromptEvaluationSummaryResponse {
	return PromptEvaluationSummaryResponse{
		WorkspaceID: uuidToString(workspaceID),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		LastRunAt:   timestampToString(row.LastRunAt),
		Metrics: map[string]any{
			"总用例数":     row.TotalCases,
			"启用用例数":    row.ActiveCases,
			"已评估用例数":   row.EvaluatedCases,
			"通过数":      row.PassedCases,
			"失败数":      row.FailedCases,
			"通过率":      row.PassRate,
			"总耗时毫秒":    row.TotalDurationMs,
			"平均耗时毫秒":   row.AverageDurationMs,
			"输入token":  row.InputTokens,
			"输出token":  row.OutputTokens,
			"预估成本":     row.EstimatedCost,
			"Agent运行数": row.AgentRuns,
			"本地运行数":    row.LocalRuns,
			"待确认优化候选":  row.PendingCandidates,
			"已发布优化候选":  row.PublishedCandidates,
		},
		Assets: map[string]int64{
			"资产总数":  row.TotalAssets,
			"启用资产数": row.ActiveAssets,
			"数据集":   row.DatasetAssets,
			"测试套件":  row.TestSuiteAssets,
			"实验":    row.ExperimentAssets,
			"优化运行":  row.OptimizationAssets,
			"结构化用例": row.TotalCases,
			"启用用例":  row.ActiveCases,
		},
		RunStatus: map[string]int64{
			"运行总数":    row.TotalRuns,
			"本地渲染":    row.LocalRuns,
			"Agent执行": row.AgentRuns,
			"已入队":     row.QueuedRuns,
			"运行中":     row.RunningRuns,
			"通过":      row.PassedRuns,
			"未通过":     row.NotPassedRuns,
			"失败":      row.FailedRuns,
			"已取消":     row.CancelledRuns,
		},
		Candidates: map[string]int64{
			"候选总数": row.TotalCandidates,
			"待确认":  row.PendingCandidates,
			"已发布":  row.PublishedCandidates,
			"已拒绝":  row.RejectedCandidates,
		},
	}
}

func validPromptEvaluationAssetType(assetType string) bool {
	return assetType == promptEvaluationAssetDataset ||
		assetType == promptEvaluationAssetTestSuite ||
		assetType == promptEvaluationAssetExperiment ||
		assetType == promptEvaluationAssetOptimize
}

func jsonObjectField(w http.ResponseWriter, raw json.RawMessage, field string) ([]byte, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		writeError(w, http.StatusBadRequest, field+" must be a JSON object")
		return nil, false
	}
	return raw, true
}

func (h *Handler) promptEvaluationPromptID(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, raw json.RawMessage, fallback pgtype.UUID) (pgtype.UUID, bool) {
	if len(raw) == 0 {
		return fallback, true
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return pgtype.UUID{}, true
	}
	var promptID string
	if err := json.Unmarshal(raw, &promptID); err != nil {
		writeError(w, http.StatusBadRequest, "prompt_id must be a string or null")
		return pgtype.UUID{}, false
	}
	if promptID == "" {
		return pgtype.UUID{}, true
	}
	promptUUID, ok := parseUUIDOrBadRequest(w, promptID, "prompt_id")
	if !ok {
		return pgtype.UUID{}, false
	}
	if _, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{ID: promptUUID, WorkspaceID: workspaceID}); err != nil {
		writeError(w, http.StatusBadRequest, "prompt_id does not belong to this workspace")
		return pgtype.UUID{}, false
	}
	return promptUUID, true
}

func (h *Handler) ListPromptEvaluationAssets(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var assetType pgtype.Text
	if value := r.URL.Query().Get("asset_type"); value != "" {
		if !validPromptEvaluationAssetType(value) {
			writeError(w, http.StatusBadRequest, "asset_type must be 数据集, 测试套件, 实验 or 优化运行")
			return
		}
		assetType = pgtype.Text{String: value, Valid: true}
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptLibraryStatus(value) {
			writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}
	var promptID pgtype.UUID
	if value := r.URL.Query().Get("prompt_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "prompt_id")
		if !ok {
			return
		}
		promptID = parsed
	}
	assets, err := h.Queries.ListPromptEvaluationAssets(r.Context(), db.ListPromptEvaluationAssetsParams{
		WorkspaceID: workspaceUUID,
		AssetType:   assetType,
		Status:      status,
		PromptID:    promptID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation assets")
		return
	}
	resp := make([]PromptEvaluationAssetResponse, len(assets))
	for i, asset := range assets {
		resp[i] = promptEvaluationAssetToResponse(asset)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) GetPromptEvaluationAsset(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationAssetToResponse(asset))
}

func (h *Handler) ListPromptEvaluationCases(w http.ResponseWriter, r *http.Request) {
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
		if !validPromptLibraryStatus(value) {
			writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}
	cases, err := h.Queries.ListPromptEvaluationCases(r.Context(), db.ListPromptEvaluationCasesParams{
		WorkspaceID: workspaceUUID,
		AssetID:     assetID,
		Status:      status,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation cases")
		return
	}
	resp := make([]PromptEvaluationCaseResponse, len(cases))
	for i, item := range cases {
		resp[i] = promptEvaluationCaseToResponse(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

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
	limit := int32(50)
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = int32(parsed)
	}
	runs, err := h.Queries.ListPromptEvaluationRuns(r.Context(), db.ListPromptEvaluationRunsParams{
		WorkspaceID: workspaceUUID,
		AssetID:     assetID,
		Status:      status,
		Limit:       limit,
	})
	if err != nil {
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
	row, err := h.Queries.GetPromptEvaluationSummary(r.Context(), workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation summary")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationSummaryToResponse(workspaceUUID, row))
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
	run, err := h.Queries.GetPromptEvaluationRunInWorkspace(r.Context(), db.GetPromptEvaluationRunInWorkspaceParams{ID: runID, WorkspaceID: workspaceUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation run")
		return
	}
	trials, err := h.Queries.ListPromptEvaluationTrialsByRun(r.Context(), db.ListPromptEvaluationTrialsByRunParams{
		RunID:       run.ID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation trials")
		return
	}
	trialResp := make([]PromptEvaluationTrialResponse, len(trials))
	for i, trial := range trials {
		trialResp[i] = promptEvaluationTrialToResponse(trial)
	}

	usageResp := []PromptEvaluationTaskUsageResponse{}
	messageResp := []protocol.TaskMessagePayload{}
	traceResp := []TaskTraceEventResponse{}
	if run.TaskID.Valid {
		usages, err := h.Queries.GetTaskUsage(r.Context(), run.TaskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load task usage")
			return
		}
		usageResp = make([]PromptEvaluationTaskUsageResponse, len(usages))
		for i, usage := range usages {
			usageResp[i] = promptEvaluationTaskUsageToResponse(usage)
		}

		messages, err := h.Queries.ListTaskMessages(r.Context(), run.TaskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load task messages")
			return
		}
		issueID := ""
		if task, err := h.Queries.GetAgentTask(r.Context(), run.TaskID); err == nil {
			issueID = uuidToString(task.IssueID)
		}
		messageResp = make([]protocol.TaskMessagePayload, len(messages))
		for i, message := range messages {
			messageResp[i] = taskMessageToPayload(message, uuidToString(run.TaskID), issueID)
		}

		traceEvents, err := h.Queries.ListTaskTraceEventsByTask(r.Context(), run.TaskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load task trace events")
			return
		}
		traceResp = make([]TaskTraceEventResponse, len(traceEvents))
		for i, event := range traceEvents {
			traceResp[i] = taskTraceEventToResponse(event)
		}
	}

	writeJSON(w, http.StatusOK, PromptEvaluationRunEvidenceResponse{
		Run:          promptEvaluationRunToResponse(run),
		Trials:       trialResp,
		TaskUsage:    usageResp,
		TaskMessages: messageResp,
		TraceEvents:  traceResp,
		Evidence:     decodeJSONDefault(run.Evidence, map[string]any{}),
	})
}

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
		Metrics:              run.Metrics,
		Status:               "待确认",
		CreatedBy:            parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation optimization candidate")
		return
	}
	writeJSON(w, http.StatusCreated, promptEvaluationOptimizationCandidateToResponse(item))
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
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          run.TaskID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "linked agent task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load linked agent task")
		return
	}
	usages, err := h.Queries.GetTaskUsage(r.Context(), task.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load task usage")
		return
	}
	taskMessages, err := h.Queries.ListTaskMessages(r.Context(), task.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load task messages")
		return
	}
	status, passed, failed, passRate, conclusion, failureReason := promptEvaluationRunStatusFromTask(run, task)
	inputTokens, outputTokens := promptEvaluationUsageTotals(run, usages)
	durationMs := promptEvaluationTaskDurationMs(task, run)
	averageMs := int64(0)
	if run.TotalCases > 0 && durationMs > 0 {
		averageMs = durationMs / int64(run.TotalCases)
	}
	evidence := promptEvaluationTaskEvidence(run, task, usages, taskMessages)
	metrics := map[string]any{
		"总用例数":          run.TotalCases,
		"通过数":           passed,
		"失败数":           failed,
		"通过率":           passRate,
		"总耗时":           durationMs,
		"平均耗时":          averageMs,
		"输入token":       inputTokens,
		"输出token":       outputTokens,
		"预估成本":          run.EstimatedCost,
		"执行Agent":       uuidToString(task.AgentID),
		"模型":            run.Model,
		"runtime":       run.RuntimeProvider,
		"trace/task id": uuidToString(task.ID),
		"失败原因":          failureReason,
		"评估结论":          conclusion,
	}
	updated, err := h.Queries.UpdatePromptEvaluationRunFromTask(r.Context(), db.UpdatePromptEvaluationRunFromTaskParams{
		ID:                run.ID,
		WorkspaceID:       run.WorkspaceID,
		Status:            status,
		PassedCases:       passed,
		FailedCases:       failed,
		PassRate:          passRate,
		TotalDurationMs:   durationMs,
		AverageDurationMs: averageMs,
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		EstimatedCost:     run.EstimatedCost,
		FailureReason:     pgtype.Text{String: failureReason, Valid: true},
		Conclusion:        pgtype.Text{String: conclusion, Valid: true},
		Metrics:           mustJSONBytes(metrics),
		Evidence:          mustJSONBytes(evidence),
		StartedAt:         task.StartedAt,
		CompletedAt:       task.CompletedAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation run from task")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationRunToResponse(updated))
}

func (h *Handler) syncPromptEvaluationCasesFromPayload(w http.ResponseWriter, r *http.Request, qtx *db.Queries, asset db.PromptEvaluationAsset, createdBy pgtype.UUID) bool {
	if err := qtx.DeletePromptEvaluationCasesByAsset(r.Context(), db.DeletePromptEvaluationCasesByAssetParams{
		WorkspaceID: asset.WorkspaceID,
		AssetID:     asset.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to refresh prompt evaluation cases")
		return false
	}
	cases := promptEvaluationCases(decodePayloadObject(asset.Payload))
	for idx, item := range cases {
		normalized := normalizePromptEvaluationCase(idx, item)
		if _, err := qtx.CreatePromptEvaluationCase(r.Context(), db.CreatePromptEvaluationCaseParams{
			WorkspaceID:      asset.WorkspaceID,
			AssetID:          asset.ID,
			PromptID:         asset.PromptID,
			CaseIndex:        int32(idx),
			CaseName:         normalized.Name,
			Variables:        mustJSONBytes(normalized.Variables),
			ExpectedContains: mustJSONBytes(normalized.ExpectedContains),
			Input:            mustJSONBytes(normalized.Input),
			Expected:         mustJSONBytes(normalized.Expected),
			Tags:             mustJSONBytes(normalized.Tags),
			Status:           asset.Status,
			Source:           "payload",
			CreatedBy:        createdBy,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation case")
			return false
		}
	}
	return true
}

func (h *Handler) CreatePromptEvaluationAsset(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreatePromptEvaluationAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !validPromptEvaluationAssetType(req.AssetType) {
		writeError(w, http.StatusBadRequest, "asset_type must be 数据集, 测试套件, 实验 or 优化运行")
		return
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
	payload, ok := jsonObjectField(w, req.Payload, "payload")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	asset, err := qtx.CreatePromptEvaluationAsset(r.Context(), db.CreatePromptEvaluationAssetParams{
		WorkspaceID: workspaceUUID,
		Name:        req.Name,
		Description: req.Description,
		AssetType:   req.AssetType,
		CreatedBy:   parseUUID(userID),
		PromptID:    promptID,
		Payload:     payload,
		Status:      status,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an evaluation asset with this type and name already exists")
			return
		}
		if isCheckViolation(err) {
			writeError(w, http.StatusBadRequest, "prompt evaluation asset rejected: a field value failed a database constraint")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation asset")
		return
	}
	if ok := h.syncPromptEvaluationCasesFromPayload(w, r, qtx, asset, parseUUID(userID)); !ok {
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation asset")
		return
	}
	writeJSON(w, http.StatusCreated, promptEvaluationAssetToResponse(asset))
}

func (h *Handler) UpdatePromptEvaluationAsset(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	var req UpdatePromptEvaluationAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != nil && *req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.AssetType != nil && !validPromptEvaluationAssetType(*req.AssetType) {
		writeError(w, http.StatusBadRequest, "asset_type must be 数据集, 测试套件, 实验 or 优化运行")
		return
	}
	if req.Status != nil && !validPromptLibraryStatus(*req.Status) {
		writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
		return
	}
	promptID, ok := h.promptEvaluationPromptID(w, r, existing.WorkspaceID, req.PromptID, existing.PromptID)
	if !ok {
		return
	}
	payload, ok := jsonObjectField(w, req.Payload, "payload")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	asset, err := qtx.UpdatePromptEvaluationAsset(r.Context(), db.UpdatePromptEvaluationAssetParams{
		ID:          existing.ID,
		WorkspaceID: existing.WorkspaceID,
		PromptID:    promptID,
		Name:        textParam(req.Name),
		Description: textParam(req.Description),
		AssetType:   textParam(req.AssetType),
		Payload:     payload,
		Status:      textParam(req.Status),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an evaluation asset with this type and name already exists")
			return
		}
		if isCheckViolation(err) {
			writeError(w, http.StatusBadRequest, "prompt evaluation asset rejected: a field value failed a database constraint")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update prompt evaluation asset")
		return
	}
	if ok := h.syncPromptEvaluationCasesFromPayload(w, r, qtx, asset, existing.CreatedBy); !ok {
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation asset")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationAssetToResponse(asset))
}

func (h *Handler) DeletePromptEvaluationAsset(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if err := h.Queries.DeletePromptEvaluationAsset(r.Context(), db.DeletePromptEvaluationAssetParams{ID: asset.ID, WorkspaceID: asset.WorkspaceID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete prompt evaluation asset")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) RunPromptEvaluationAsset(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if !asset.PromptID.Valid {
		writeError(w, http.StatusBadRequest, "prompt_id is required to run an evaluation asset")
		return
	}
	prompt, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{
		ID:          asset.PromptID,
		WorkspaceID: asset.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "prompt_id does not belong to this workspace")
		return
	}
	payload := decodePayloadObject(asset.Payload)
	cases, ok := h.promptEvaluationCasesForAsset(w, r, asset)
	if !ok {
		return
	}
	result := buildPromptEvaluationRunResult(asset, prompt, payload, cases)
	run, ok := h.persistPromptEvaluationLocalRun(w, r, asset, result, parseUUID(userID))
	if !ok {
		return
	}
	result.TraceTaskID = uuidToString(run.ID)
	payload["最近运行"] = result
	payload["运行记录"] = appendPromptEvaluationRunHistory(payload["运行记录"], result)
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode prompt evaluation result")
		return
	}
	updated, err := h.Queries.UpdatePromptEvaluationAsset(r.Context(), db.UpdatePromptEvaluationAssetParams{
		ID:          asset.ID,
		WorkspaceID: asset.WorkspaceID,
		PromptID:    asset.PromptID,
		Payload:     payloadBytes,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save prompt evaluation result")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationAssetToResponse(updated))
}

func (h *Handler) RunPromptEvaluationAssetAgent(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if !asset.PromptID.Valid {
		writeError(w, http.StatusBadRequest, "prompt_id is required to run an evaluation asset with an agent")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	prompt, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{
		ID:          asset.PromptID,
		WorkspaceID: asset.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "prompt_id does not belong to this workspace")
		return
	}
	member, ok := h.workspaceMember(w, r, uuidToString(asset.WorkspaceID))
	if !ok {
		return
	}
	agentRow, runtimeRow, ok := h.ensurePromptEvaluationAgent(w, r, asset.WorkspaceID, parseUUID(userID), member)
	if !ok {
		return
	}

	session, err := h.Queries.CreateChatSession(r.Context(), db.CreateChatSessionParams{
		WorkspaceID: asset.WorkspaceID,
		AgentID:     agentRow.ID,
		CreatorID:   parseUUID(userID),
		Title:       "训练评估：" + asset.Name,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create training evaluation chat session")
		return
	}
	messageText := buildPromptEvaluationAgentMessage(asset, prompt, decodePayloadObject(asset.Payload))
	msg, err := h.Queries.CreateChatMessage(r.Context(), db.CreateChatMessageParams{
		ChatSessionID: session.ID,
		Role:          "user",
		Content:       messageText,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create training evaluation chat message")
		return
	}
	task, err := h.TaskService.EnqueueChatTask(r.Context(), session, parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue training evaluation agent task: "+err.Error())
		return
	}
	if err := h.Queries.LinkChatMessageToTask(r.Context(), db.LinkChatMessageToTaskParams{ID: msg.ID, TaskID: task.ID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link training evaluation message to task")
		return
	}

	payload := decodePayloadObject(asset.Payload)
	cases, ok := h.promptEvaluationCasesForAsset(w, r, asset)
	if !ok {
		return
	}
	run, ok := h.persistPromptEvaluationQueuedAgentRun(w, r, asset, agentRow, runtimeRow, task.ID, session.ID, parseUUID(userID), payload, cases)
	if !ok {
		return
	}
	runIndex := map[string]any{
		"运行时间":            time.Now().UTC().Format(time.RFC3339),
		"run_id":          uuidToString(run.ID),
		"状态":              "已入队",
		"执行Agent":         agentRow.Name,
		"agent_id":        uuidToString(agentRow.ID),
		"模型":              promptEvaluationAgentModel,
		"runtime":         runtimeRow.Provider,
		"runtime_id":      uuidToString(runtimeRow.ID),
		"trace/task id":   uuidToString(task.ID),
		"chat_session_id": uuidToString(session.ID),
		"失败原因":            "无",
		"评估结论":            "等待 Agent 执行完成",
	}
	payload["最近Agent运行"] = runIndex
	payload["Agent运行记录"] = appendPromptEvaluationAgentRunHistory(payload["Agent运行记录"], runIndex)
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode training evaluation agent run")
		return
	}
	updated, err := h.Queries.UpdatePromptEvaluationAsset(r.Context(), db.UpdatePromptEvaluationAssetParams{
		ID:          asset.ID,
		WorkspaceID: asset.WorkspaceID,
		PromptID:    asset.PromptID,
		Payload:     payloadBytes,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save training evaluation agent run")
		return
	}
	writeJSON(w, http.StatusAccepted, PromptEvaluationAgentRunResponse{
		Asset:         promptEvaluationAssetToResponse(updated),
		Run:           promptEvaluationRunToResponse(run),
		TaskID:        uuidToString(task.ID),
		ChatSessionID: uuidToString(session.ID),
		AgentID:       uuidToString(agentRow.ID),
		RuntimeID:     uuidToString(runtimeRow.ID),
		Model:         promptEvaluationAgentModel,
		Status:        "已入队",
		Message:       "真实 Agent 任务已入队；请通过 task messages、usage 和运行历史追踪结果。",
	})
}

func (h *Handler) persistPromptEvaluationLocalRun(w http.ResponseWriter, r *http.Request, asset db.PromptEvaluationAsset, result promptEvaluationRunResult, createdBy pgtype.UUID) (db.PromptEvaluationRun, bool) {
	now := time.Now()
	status := "通过"
	if result.FailedCases > 0 {
		status = "未通过"
	}
	metrics := map[string]any{
		"总用例数":    result.TotalCases,
		"通过数":     result.PassedCases,
		"失败数":     result.FailedCases,
		"通过率":     result.PassRate,
		"总耗时":     result.TotalDurationMs,
		"平均耗时":    result.AverageDurationMs,
		"输入token": result.InputTokens,
		"输出token": result.OutputTokens,
		"预估成本":    result.EstimatedCost,
		"执行Agent": result.AgentName,
		"模型":      result.Model,
		"runtime": result.Runtime,
		"失败原因":    result.FailureReason,
		"评估结论":    result.Conclusion,
	}
	run, err := h.Queries.CreatePromptEvaluationRun(r.Context(), db.CreatePromptEvaluationRunParams{
		WorkspaceID:       asset.WorkspaceID,
		AssetID:           asset.ID,
		PromptID:          asset.PromptID,
		RunKind:           "本地渲染",
		Status:            status,
		TriggerSource:     "手动",
		Model:             result.Model,
		RuntimeProvider:   result.Runtime,
		TotalCases:        int32(result.TotalCases),
		PassedCases:       int32(result.PassedCases),
		FailedCases:       int32(result.FailedCases),
		PassRate:          result.PassRate,
		TotalDurationMs:   result.TotalDurationMs,
		AverageDurationMs: result.AverageDurationMs,
		InputTokens:       int32(result.InputTokens),
		OutputTokens:      int32(result.OutputTokens),
		EstimatedCost:     result.EstimatedCost,
		FailureReason:     result.FailureReason,
		Conclusion:        result.Conclusion,
		Metrics:           mustJSONBytes(metrics),
		Evidence:          mustJSONBytes(map[string]any{"资产类型": asset.AssetType, "运行方式": "本地提示词渲染"}),
		StartedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		CompletedAt:       pgtype.Timestamptz{Time: now, Valid: true},
		CreatedBy:         createdBy,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation run")
		return db.PromptEvaluationRun{}, false
	}
	for idx, caseResult := range result.CaseResults {
		failureReason := ""
		if caseResult.Status != "通过" {
			failureReason = result.FailureReason
		}
		if _, err := h.Queries.CreatePromptEvaluationTrial(r.Context(), db.CreatePromptEvaluationTrialParams{
			RunID:          run.ID,
			WorkspaceID:    asset.WorkspaceID,
			AssetID:        asset.ID,
			CaseIndex:      int32(idx),
			CaseName:       caseResult.Name,
			Status:         caseResult.Status,
			Input:          mustJSONBytes(map[string]any{"变量": caseResult.Variables}),
			Expected:       mustJSONBytes(map[string]any{"期望包含": caseResult.ExpectedContains}),
			Output:         mustJSONBytes(map[string]any{"已匹配": caseResult.MatchedContains, "使用变量": caseResult.UsedVariables, "缺失变量": caseResult.MissingVariables}),
			RenderedPrompt: caseResult.RenderedPrompt,
			InputTokens:    int32(estimatePromptEvaluationTokens(caseResult.RenderedPrompt)),
			OutputTokens:   int32(estimatePromptEvaluationTokens(caseResult.RenderedPrompt)),
			DurationMs:     result.AverageDurationMs,
			FailureReason:  failureReason,
			Evidence:       mustJSONBytes(map[string]any{"run_id": uuidToString(run.ID), "trace/task id": uuidToString(run.ID)}),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation trial")
			return db.PromptEvaluationRun{}, false
		}
	}
	return run, true
}

func (h *Handler) persistPromptEvaluationQueuedAgentRun(w http.ResponseWriter, r *http.Request, asset db.PromptEvaluationAsset, agent db.Agent, runtime db.AgentRuntime, taskID pgtype.UUID, chatSessionID pgtype.UUID, createdBy pgtype.UUID, payload map[string]any, cases []map[string]any) (db.PromptEvaluationRun, bool) {
	run, err := h.Queries.CreatePromptEvaluationRun(r.Context(), db.CreatePromptEvaluationRunParams{
		WorkspaceID:       asset.WorkspaceID,
		AssetID:           asset.ID,
		PromptID:          asset.PromptID,
		RunKind:           "Agent执行",
		Status:            "已入队",
		TriggerSource:     "手动",
		AgentID:           agent.ID,
		RuntimeID:         runtime.ID,
		TaskID:            taskID,
		ChatSessionID:     chatSessionID,
		Model:             promptEvaluationAgentModel,
		RuntimeProvider:   runtime.Provider,
		TotalCases:        int32(len(cases)),
		PassedCases:       0,
		FailedCases:       0,
		PassRate:          0,
		TotalDurationMs:   0,
		AverageDurationMs: 0,
		InputTokens:       int32(estimatePromptEvaluationTokens(string(mustJSONBytes(payload)))),
		OutputTokens:      0,
		EstimatedCost:     0,
		FailureReason:     "无",
		Conclusion:        "等待 Agent 执行完成",
		Metrics: mustJSONBytes(map[string]any{
			"总用例数":          len(cases),
			"通过数":           0,
			"失败数":           0,
			"通过率":           0,
			"执行Agent":       agent.Name,
			"模型":            promptEvaluationAgentModel,
			"runtime":       runtime.Provider,
			"trace/task id": uuidToString(taskID),
			"评估结论":          "等待 Agent 执行完成",
		}),
		Evidence: mustJSONBytes(map[string]any{
			"task_id":         uuidToString(taskID),
			"chat_session_id": uuidToString(chatSessionID),
			"agent_id":        uuidToString(agent.ID),
			"runtime_id":      uuidToString(runtime.ID),
		}),
		StartedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CreatedBy: createdBy,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create queued prompt evaluation run")
		return db.PromptEvaluationRun{}, false
	}
	for idx, c := range cases {
		name := stringFromAny(firstValue(c, "name", "名称"))
		if name == "" {
			name = "用例 " + strconv.Itoa(idx+1)
		}
		if _, err := h.Queries.CreatePromptEvaluationTrial(r.Context(), db.CreatePromptEvaluationTrialParams{
			RunID:         run.ID,
			WorkspaceID:   asset.WorkspaceID,
			AssetID:       asset.ID,
			CaseIndex:     int32(idx),
			CaseName:      name,
			Status:        "待执行",
			Input:         mustJSONBytes(map[string]any{"变量": firstValue(c, "variables", "变量", "输入变量")}),
			Expected:      mustJSONBytes(map[string]any{"期望包含": firstValue(c, "expected_contains", "期望包含", "期望")}),
			Output:        mustJSONBytes(map[string]any{}),
			FailureReason: "等待 Agent 执行完成",
			Evidence:      mustJSONBytes(map[string]any{"run_id": uuidToString(run.ID), "task_id": uuidToString(taskID)}),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create queued prompt evaluation trial")
			return db.PromptEvaluationRun{}, false
		}
	}
	return run, true
}

func (h *Handler) promptEvaluationCasesForAsset(w http.ResponseWriter, r *http.Request, asset db.PromptEvaluationAsset) ([]map[string]any, bool) {
	rows, err := h.Queries.ListPromptEvaluationCases(r.Context(), db.ListPromptEvaluationCasesParams{
		WorkspaceID: asset.WorkspaceID,
		AssetID:     asset.ID,
		Status:      pgtype.Text{String: "启用", Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation cases")
		return nil, false
	}
	if len(rows) == 0 {
		return promptEvaluationCases(decodePayloadObject(asset.Payload)), true
	}
	cases := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		cases = append(cases, map[string]any{
			"名称":   row.CaseName,
			"变量":   decodeJSONDefault(row.Variables, map[string]any{}),
			"期望包含": decodeJSONDefault(row.ExpectedContains, []any{}),
			"输入":   decodeJSONDefault(row.Input, map[string]any{}),
			"期望":   decodeJSONDefault(row.Expected, map[string]any{}),
		})
	}
	return cases, true
}

func promptEvaluationRunStatusFromTask(run db.PromptEvaluationRun, task db.AgentTaskQueue) (string, int32, int32, float64, string, string) {
	switch task.Status {
	case "completed":
		total := run.TotalCases
		return "通过", total, 0, promptEvaluationPassRate(total, 0), "Agent 执行完成，等待验收者复核输出质量", "无"
	case "failed":
		total := run.TotalCases
		reason := "Agent 执行失败"
		if task.Error.Valid && strings.TrimSpace(task.Error.String) != "" {
			reason = task.Error.String
		}
		return "失败", 0, total, 0, "Agent 执行失败，需要查看 task 日志和失败原因", reason
	case "cancelled":
		return "已取消", run.PassedCases, run.FailedCases, run.PassRate, "Agent 执行已取消", "任务被取消"
	case "running":
		return "运行中", run.PassedCases, run.FailedCases, run.PassRate, "Agent 正在执行", "无"
	default:
		return "已入队", run.PassedCases, run.FailedCases, run.PassRate, "等待 Agent 执行完成", "无"
	}
}

func promptEvaluationPassRate(passed int32, failed int32) float64 {
	total := passed + failed
	if total == 0 {
		return 0
	}
	return float64(passed) / float64(total)
}

func promptEvaluationUsageTotals(run db.PromptEvaluationRun, usages []db.TaskUsage) (int32, int32) {
	input := int64(0)
	output := int64(0)
	for _, usage := range usages {
		input += usage.InputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
		output += usage.OutputTokens
	}
	if input == 0 {
		input = int64(run.InputTokens)
	}
	if output == 0 {
		output = int64(run.OutputTokens)
	}
	return clampInt32(input), clampInt32(output)
}

func promptEvaluationTaskDurationMs(task db.AgentTaskQueue, run db.PromptEvaluationRun) int64 {
	if task.StartedAt.Valid && task.CompletedAt.Valid {
		duration := task.CompletedAt.Time.Sub(task.StartedAt.Time).Milliseconds()
		if duration > 0 {
			return duration
		}
	}
	if task.StartedAt.Valid && task.Status == "running" {
		duration := time.Since(task.StartedAt.Time).Milliseconds()
		if duration > 0 {
			return duration
		}
	}
	return run.TotalDurationMs
}

func validPromptEvaluationOptimizationCandidateStatus(status string) bool {
	return status == "待确认" || status == "已发布" || status == "已拒绝"
}

func promptEvaluationRunHasFailure(run db.PromptEvaluationRun) bool {
	if run.FailedCases > 0 {
		return true
	}
	if run.Status == "未通过" || run.Status == "失败" {
		return true
	}
	reason := strings.TrimSpace(run.FailureReason)
	return reason != "" && reason != "无"
}

func promptEvaluationRunFailedCaseCount(run db.PromptEvaluationRun, trials []db.PromptEvaluationTrial) int32 {
	if run.FailedCases > 0 {
		return run.FailedCases
	}
	failed := int32(0)
	for _, trial := range trials {
		if trial.Status == "未通过" || trial.Status == "失败" {
			failed++
		}
	}
	if failed > 0 {
		return failed
	}
	if run.TotalCases > 0 && (run.Status == "未通过" || run.Status == "失败") {
		return run.TotalCases
	}
	return 1
}

func buildPromptEvaluationCandidateFailureSummary(run db.PromptEvaluationRun, trials []db.PromptEvaluationTrial) map[string]any {
	trialSummaries := make([]map[string]any, 0, len(trials))
	for _, trial := range trials {
		if trial.Status == "通过" {
			continue
		}
		trialSummaries = append(trialSummaries, map[string]any{
			"用例序号":  trial.CaseIndex,
			"用例名称":  trial.CaseName,
			"状态":    trial.Status,
			"失败原因":  trial.FailureReason,
			"输入":    decodeJSONDefault(trial.Input, map[string]any{}),
			"期望":    decodeJSONDefault(trial.Expected, map[string]any{}),
			"输出":    decodeJSONDefault(trial.Output, map[string]any{}),
			"渲染提示词": trial.RenderedPrompt,
		})
	}
	if len(trialSummaries) == 0 && len(trials) > 0 {
		for _, trial := range trials {
			trialSummaries = append(trialSummaries, map[string]any{
				"用例序号": trial.CaseIndex,
				"用例名称": trial.CaseName,
				"状态":   trial.Status,
				"输入":   decodeJSONDefault(trial.Input, map[string]any{}),
				"期望":   decodeJSONDefault(trial.Expected, map[string]any{}),
			})
		}
	}
	return map[string]any{
		"run_id":        uuidToString(run.ID),
		"asset_id":      uuidToString(run.AssetID),
		"run_kind":      run.RunKind,
		"状态":            run.Status,
		"总用例数":          run.TotalCases,
		"通过数":           run.PassedCases,
		"失败数":           promptEvaluationRunFailedCaseCount(run, trials),
		"通过率":           run.PassRate,
		"模型":            run.Model,
		"runtime":       run.RuntimeProvider,
		"trace/task id": uuidToPtr(run.TaskID),
		"失败原因":          run.FailureReason,
		"评估结论":          run.Conclusion,
		"失败用例":          trialSummaries,
		"evidence":      decodeJSONDefault(run.Evidence, map[string]any{}),
		"生成说明":          "基于结构化运行记录和失败用例生成优化候选；候选不会自动替换生产提示词。",
	}
}

func buildPromptEvaluationCandidateContent(prompt db.PromptLibraryItem, run db.PromptEvaluationRun, sourceSummary map[string]any) (string, string) {
	failureReason := strings.TrimSpace(run.FailureReason)
	if failureReason == "" {
		failureReason = "结构化运行记录显示存在失败用例，需要补充边界、输出格式和验收约束。"
	}
	failedCases, _ := sourceSummary["失败用例"].([]map[string]any)
	rationale := "基于失败用例补充中文输出约束、失败处理要求、证据字段和验收口径；原提示词不被自动替换，必须人工确认后发布。"
	lines := []string{
		strings.TrimSpace(prompt.Content),
		"",
		"【优化候选：失败用例修复建议】",
		"来源运行：" + uuidToString(run.ID),
		"失败原因：" + failureReason,
		"失败用例数：" + strconv.Itoa(int(promptEvaluationRunFailedCaseCount(run, nil))),
		"",
		"请在后续执行中严格遵守：",
		"1. 全部输出使用中文，避免英文状态词和未解释的内部缩写。",
		"2. 对每个输入先复述目标、边界、影响范围和验收条件。",
		"3. 当信息不足时，先提出需要团队确认的问题，不要直接假设结论。",
		"4. 输出必须包含可观测证据：耗时、执行 Agent、模型、trace/task id、失败原因和评估结论。",
		"5. 如果触发失败场景，明确指出失败用例、缺失变量、未命中期望和下一步修复建议。",
	}
	if len(failedCases) > 0 {
		lines = append(lines, "", "失败用例摘要：")
		for _, item := range failedCases {
			name := stringFromAny(item["用例名称"])
			reason := stringFromAny(item["失败原因"])
			if name == "" {
				name = "未命名用例"
			}
			if reason == "" {
				reason = failureReason
			}
			lines = append(lines, "- "+name+"："+reason)
		}
	}
	lines = append(lines, "", "人工发布要求：发布前必须由验收者确认该候选不会降低原有通过用例质量。")
	return strings.Join(lines, "\n"), rationale
}

func buildPromptEvaluationCandidateName(prompt db.PromptLibraryItem, run db.PromptEvaluationRun) string {
	return prompt.Name + " 优化候选 " + run.CreatedAt.Time.Format("20060102") + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func buildPromptEvaluationSourcePromptSnapshot(prompt db.PromptLibraryItem) map[string]any {
	return map[string]any{
		"prompt_id": uuidToString(prompt.ID),
		"名称":        prompt.Name,
		"类型":        prompt.PromptType,
		"版本":        prompt.Version,
		"状态":        prompt.Status,
		"变量":        decodeJSONDefault(prompt.Variables, []any{}),
		"标签":        decodeJSONDefault(prompt.Tags, []any{}),
		"内容摘要":      truncatePromptEvaluationEvidence(prompt.Content, 1200),
	}
}

func buildPromptEvaluationPublishedPromptName(prompt db.PromptLibraryItem) string {
	return prompt.Name + " 优化发布 " + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func buildPromptEvaluationPublishedPromptDescription(candidate db.PromptEvaluationOptimizationCandidate, prompt db.PromptLibraryItem) string {
	parts := []string{
		"由训练与评估优化候选人工确认发布。",
		"来源提示词：" + prompt.Name + " v" + strconv.Itoa(int(prompt.Version)) + "。",
		"来源运行：" + uuidToString(candidate.RunID) + "。",
	}
	if candidate.Rationale != "" {
		parts = append(parts, "优化依据："+candidate.Rationale)
	}
	return strings.Join(parts, " ")
}

func buildPromptEvaluationPublishedPromptTags(raw []byte) []byte {
	tags := stringListFromAny(decodeJSONDefault(raw, []any{}))
	seen := map[string]bool{}
	next := make([]string, 0, len(tags)+3)
	for _, tag := range append(tags, "优化发布", "人工确认", "训练与评估") {
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		next = append(next, tag)
	}
	return mustJSONBytes(next)
}

func promptEvaluationTaskEvidence(run db.PromptEvaluationRun, task db.AgentTaskQueue, usages []db.TaskUsage, messages []db.TaskMessage) map[string]any {
	usageRows := make([]map[string]any, 0, len(usages))
	for _, usage := range usages {
		usageRows = append(usageRows, map[string]any{
			"provider":           usage.Provider,
			"model":              usage.Model,
			"input_tokens":       usage.InputTokens,
			"output_tokens":      usage.OutputTokens,
			"cache_read_tokens":  usage.CacheReadTokens,
			"cache_write_tokens": usage.CacheWriteTokens,
		})
	}
	messageSummary := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		messageSummary = append(messageSummary, map[string]any{
			"seq":     message.Seq,
			"type":    message.Type,
			"tool":    message.Tool,
			"content": truncatePromptEvaluationEvidence(promptEvaluationTextValue(message.Content), 800),
		})
	}
	return map[string]any{
		"run_id":        uuidToString(run.ID),
		"task_id":       uuidToString(task.ID),
		"task_status":   task.Status,
		"task_result":   decodeJSONDefault(task.Result, map[string]any{}),
		"task_error":    promptEvaluationTextValue(task.Error),
		"session_id":    promptEvaluationTextValue(task.SessionID),
		"work_dir":      promptEvaluationTextValue(task.WorkDir),
		"started_at":    timestampToString(task.StartedAt),
		"completed_at":  timestampToString(task.CompletedAt),
		"usage":         usageRows,
		"task_messages": messageSummary,
		"同步时间":          time.Now().UTC().Format(time.RFC3339),
		"同步说明":          "从 agent_task_queue、task_usage 和 task_message 回写结构化训练评估运行记录",
	}
}

func truncatePromptEvaluationEvidence(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}

func promptEvaluationTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func clampInt32(value int64) int32 {
	if value > int64(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	if value < 0 {
		return 0
	}
	return int32(value)
}

func (h *Handler) loadPromptEvaluationAsset(w http.ResponseWriter, r *http.Request) (db.PromptEvaluationAsset, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return db.PromptEvaluationAsset{}, false
	}
	assetID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation asset id")
	if !ok {
		return db.PromptEvaluationAsset{}, false
	}
	asset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{
		ID:          assetID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation asset not found")
			return db.PromptEvaluationAsset{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation asset")
		return db.PromptEvaluationAsset{}, false
	}
	return asset, true
}

func (h *Handler) ensurePromptEvaluationAgent(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, ownerID pgtype.UUID, member db.Member) (db.Agent, db.AgentRuntime, bool) {
	runtime, ok := h.selectPromptEvaluationRuntime(w, r, workspaceID, member)
	if !ok {
		return db.Agent{}, db.AgentRuntime{}, false
	}
	agents, err := h.Queries.ListAgents(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agents for training evaluation")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	instructions := promptEvaluationAgentInstructions()
	for _, existing := range agents {
		if existing.Name != promptEvaluationAgentName {
			continue
		}
		if uuidToString(existing.RuntimeID) == uuidToString(runtime.ID) &&
			existing.Model.String == promptEvaluationAgentModel &&
			existing.Instructions == instructions {
			return existing, runtime, true
		}
		updated, err := h.Queries.UpdateAgent(r.Context(), db.UpdateAgentParams{
			ID:           existing.ID,
			RuntimeMode:  pgtype.Text{String: runtime.RuntimeMode, Valid: true},
			RuntimeID:    runtime.ID,
			Instructions: pgtype.Text{String: instructions, Valid: true},
			Model:        pgtype.Text{String: promptEvaluationAgentModel, Valid: true},
			Status:       pgtype.Text{String: "idle", Valid: true},
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update training evaluation agent")
			return db.Agent{}, db.AgentRuntime{}, false
		}
		return updated, runtime, true
	}

	created, err := h.Queries.CreateAgent(r.Context(), db.CreateAgentParams{
		WorkspaceID:        workspaceID,
		Name:               promptEvaluationAgentName,
		Description:        "训练与评估模块自动创建，用于真实执行提示词、数据集和测试套件。",
		RuntimeMode:        runtime.RuntimeMode,
		RuntimeConfig:      []byte("{}"),
		RuntimeID:          runtime.ID,
		Visibility:         "workspace",
		MaxConcurrentTasks: 1,
		OwnerID:            ownerID,
		Instructions:       instructions,
		CustomEnv:          []byte("{}"),
		CustomArgs:         []byte("[]"),
		Model:              pgtype.Text{String: promptEvaluationAgentModel, Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create training evaluation agent")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	return created, runtime, true
}

func (h *Handler) selectPromptEvaluationRuntime(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, member db.Member) (db.AgentRuntime, bool) {
	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes for training evaluation")
		return db.AgentRuntime{}, false
	}
	for _, runtime := range runtimes {
		if strings.EqualFold(runtime.Provider, "codebuddy") && runtime.Status == "online" && canUseRuntimeForAgent(member, runtime) {
			return runtime, true
		}
	}
	writeError(w, http.StatusServiceUnavailable, "CodeBuddy runtime is not ready; start multica daemon with codebuddy and wait for provider=codebuddy status=online")
	return db.AgentRuntime{}, false
}

func promptEvaluationAgentInstructions() string {
	return "你是 Multica 训练与评估 Agent。你只负责执行当前提示词评估任务，必须使用中文输出。输出必须包含：执行结论、逐用例结果、失败原因、改进建议、可复盘证据。不要修改业务代码，不要创建 issue，不要泄露密钥。"
}

func buildPromptEvaluationAgentMessage(asset db.PromptEvaluationAsset, prompt db.PromptLibraryItem, payload map[string]any) string {
	payloadBytes, _ := json.MarshalIndent(payload, "", "  ")
	return strings.Join([]string{
		"请执行一次 Multica 训练与评估运行。",
		"",
		"【资产】",
		"名称：" + asset.Name,
		"类型：" + asset.AssetType,
		"",
		"【提示词】",
		"名称：" + prompt.Name,
		"内容：",
		prompt.Content,
		"",
		"【评估数据】",
		string(payloadBytes),
		"",
		"【输出要求】",
		"1. 全部使用中文。",
		"2. 逐条说明用例是否通过、失败原因和证据。",
		"3. 给出中文指标：总用例数、通过数、失败数、通过率、总耗时、平均耗时、输入token、输出token、预估成本、执行Agent、模型、runtime、trace/task id、失败原因、评估结论。",
		"4. 如果需要优化提示词，只输出候选建议，不要自动替换生产版本。",
	}, "\n")
}

func decodePayloadObject(raw []byte) map[string]any {
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

func mustJSONBytes(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

func buildPromptEvaluationRunResult(asset db.PromptEvaluationAsset, prompt db.PromptLibraryItem, payload map[string]any, cases []map[string]any) promptEvaluationRunResult {
	started := time.Now()
	if len(cases) == 0 {
		cases = promptEvaluationCases(payload)
	}
	results := make([]promptEvaluationCaseRunResult, 0, len(cases))
	passed := 0
	missingCount := 0
	inputTokens := 0
	outputTokens := 0
	for idx, c := range cases {
		name := stringFromAny(firstValue(c, "name", "名称"))
		if name == "" {
			name = "用例 " + strconv.Itoa(idx+1)
		}
		variables := stringMapFromAny(firstValue(c, "variables", "变量", "输入变量"))
		expected := stringListFromAny(firstValue(c, "expected_contains", "期望包含", "期望"))
		rendered, used, missing := renderPromptContent(prompt.Content, prompt.Variables, variables)
		inputTokens += estimatePromptEvaluationTokens(prompt.Content)
		outputTokens += estimatePromptEvaluationTokens(rendered)
		matched := make([]string, 0, len(expected))
		for _, expectedText := range expected {
			if expectedText != "" && strings.Contains(rendered, expectedText) {
				matched = append(matched, expectedText)
			}
		}
		status := "通过"
		if len(missing) > 0 || len(matched) != len(expected) {
			status = "失败"
		} else {
			passed++
		}
		missingCount += len(missing)
		results = append(results, promptEvaluationCaseRunResult{
			Name:             name,
			Status:           status,
			Variables:        variables,
			RenderedPrompt:   rendered,
			UsedVariables:    used,
			MissingVariables: missing,
			ExpectedContains: expected,
			MatchedContains:  matched,
		})
	}
	durationMs := time.Since(started).Milliseconds()
	if durationMs < 1 {
		durationMs = 1
	}
	totalCases := len(results)
	failed := totalCases - passed
	passRate := 0.0
	averageMs := int64(0)
	if totalCases > 0 {
		passRate = float64(passed) / float64(totalCases)
		averageMs = durationMs / int64(totalCases)
	}
	failureReason := "无"
	conclusion := "通过"
	if failed > 0 {
		failureReason = "存在缺失变量或期望内容未命中"
		conclusion = "未通过"
	}
	return promptEvaluationRunResult{
		RunAt:             time.Now().UTC().Format(time.RFC3339),
		AssetType:         asset.AssetType,
		PromptName:        prompt.Name,
		TotalCases:        totalCases,
		PassedCases:       passed,
		FailedCases:       failed,
		PassRate:          passRate,
		TotalDurationMs:   durationMs,
		AverageDurationMs: averageMs,
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		EstimatedCost:     0,
		AgentName:         "本地提示词渲染器",
		Model:             "本地模板渲染",
		Runtime:           "server",
		TraceTaskID:       "未创建 Agent 任务",
		FailureReason:     failureReason,
		Conclusion:        conclusion,
		MissingVarCount:   missingCount,
		CaseResults:       results,
	}
}

func promptEvaluationCases(payload map[string]any) []map[string]any {
	raw := firstValue(payload, "cases", "test_cases", "用例", "测试用例", "数据集", "training_cases", "evaluation_cases", "用例集")
	if arr, ok := raw.([]any); ok {
		cases := make([]map[string]any, 0, len(arr))
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				cases = append(cases, m)
			}
		}
		if len(cases) > 0 {
			return cases
		}
	}
	return []map[string]any{{"名称": "默认用例", "变量": firstValue(payload, "variables", "变量", "输入变量")}}
}

func normalizePromptEvaluationCase(index int, item map[string]any) normalizedPromptEvaluationCase {
	name := stringFromAny(firstValue(item, "name", "名称", "case_name", "用例名称"))
	if name == "" {
		name = "用例 " + strconv.Itoa(index+1)
	}
	variables := stringMapFromAny(firstValue(item, "variables", "变量", "输入变量"))
	expectedContains := stringListFromAny(firstValue(item, "expected_contains", "期望包含", "期望"))
	input := map[string]any{
		"变量":   variables,
		"原始输入": firstValue(item, "input", "输入"),
	}
	expected := map[string]any{
		"期望包含": expectedContains,
		"原始期望": firstValue(item, "expected", "期望"),
	}
	return normalizedPromptEvaluationCase{
		Name:             name,
		Variables:        variables,
		ExpectedContains: expectedContains,
		Input:            input,
		Expected:         expected,
		Tags:             stringListFromAny(firstValue(item, "tags", "标签")),
	}
}

func estimatePromptEvaluationTokens(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	tokens := len([]rune(value)) / 4
	if tokens < 1 {
		return 1
	}
	return tokens
}

func renderPromptContent(content string, variablesRaw []byte, values map[string]string) (string, []string, []string) {
	defaults := promptVariableDefaults(variablesRaw)
	usedSet := map[string]bool{}
	missingSet := map[string]bool{}
	rendered := promptTemplateVariablePattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := promptTemplateVariablePattern.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		name := strings.TrimSpace(parts[1])
		usedSet[name] = true
		if value, ok := values[name]; ok {
			return value
		}
		if value, ok := defaults[name]; ok {
			return value
		}
		missingSet[name] = true
		return match
	})
	return rendered, sortedBoolKeys(usedSet), sortedBoolKeys(missingSet)
}

func promptVariableDefaults(raw []byte) map[string]string {
	var variables []map[string]any
	_ = json.Unmarshal(raw, &variables)
	defaults := map[string]string{}
	for _, variable := range variables {
		name := stringFromAny(variable["name"])
		if name == "" {
			continue
		}
		if value, ok := variable["default_value"]; ok {
			defaults[name] = stringFromAny(value)
		}
	}
	return defaults
}

func appendPromptEvaluationRunHistory(raw any, result promptEvaluationRunResult) []any {
	history, _ := raw.([]any)
	next := append([]any{result}, history...)
	if len(next) > 20 {
		next = next[:20]
	}
	return next
}

func appendPromptEvaluationAgentRunHistory(raw any, result map[string]any) []any {
	history, _ := raw.([]any)
	next := append([]any{result}, history...)
	if len(next) > 20 {
		next = next[:20]
	}
	return next
}

func firstValue(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func stringMapFromAny(value any) map[string]string {
	result := map[string]string{}
	if m, ok := value.(map[string]any); ok {
		for key, raw := range m {
			result[key] = stringFromAny(raw)
		}
	}
	return result
}

func stringListFromAny(value any) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(stringFromAny(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func sortedBoolKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
