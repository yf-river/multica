package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"sort"
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
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	promptEvaluationAssetDataset         = "数据集"
	promptEvaluationAssetTestSuite       = "测试套件"
	promptEvaluationAssetExperiment      = "实验"
	promptEvaluationAssetOptimize        = "优化运行"
	promptEvaluationAssetProfileV1       = "multica.training_evaluation.asset_profile.v1"
	promptEvaluationAgentName            = "Multica 训练评估 Agent"
	defaultPromptEvaluationAgentProvider = "codex"
	defaultPromptEvaluationAgentModel    = "gpt-5.3-codex-spark"
	fallbackPromptEvaluationAgentModel   = "gpt-5.4-mini"
	promptEvaluationRuntimeFreshTTL      = 2 * time.Minute
	promptEvaluationRuntimeLimitTTL      = 10 * time.Minute
)

var promptTemplateVariablePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)

func promptEvaluationAgentModel() string {
	if value := strings.TrimSpace(os.Getenv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL")); value != "" {
		return value
	}
	return defaultPromptEvaluationAgentModel
}

func promptEvaluationAgentProvider() string {
	if value := strings.TrimSpace(os.Getenv("MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER")); value != "" {
		return strings.ToLower(value)
	}
	return defaultPromptEvaluationAgentProvider
}

type PromptEvaluationAssetResponse struct {
	ID                       string  `json:"id"`
	WorkspaceID              string  `json:"workspace_id"`
	PromptID                 *string `json:"prompt_id"`
	Name                     string  `json:"name"`
	Description              string  `json:"description"`
	AssetType                string  `json:"asset_type"`
	Payload                  any     `json:"payload"`
	Status                   string  `json:"status"`
	CreatedBy                *string `json:"created_by"`
	CreatedAt                string  `json:"created_at"`
	UpdatedAt                string  `json:"updated_at"`
	StructureSchema          string  `json:"structure_schema"`
	StructuredCaseCount      int32   `json:"structured_case_count"`
	StructuredVariableCount  int32   `json:"structured_variable_count"`
	StructuredAssertionCount int32   `json:"structured_assertion_count"`
	LinkedDatasetCount       int32   `json:"linked_dataset_count"`
	LinkedPromptCount        int32   `json:"linked_prompt_count"`
	EvaluationDimensionCount int32   `json:"evaluation_dimension_count"`
	DatasetRowCount          int32   `json:"dataset_row_count"`
	TestSuiteCaseCount       int32   `json:"test_suite_case_count"`
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

type CreatePromptEvaluationCaseRequest struct {
	AssetID          string          `json:"asset_id"`
	PromptID         json.RawMessage `json:"prompt_id"`
	CaseIndex        *int32          `json:"case_index"`
	CaseName         string          `json:"case_name"`
	Variables        json.RawMessage `json:"variables"`
	ExpectedContains json.RawMessage `json:"expected_contains"`
	Input            json.RawMessage `json:"input"`
	Expected         json.RawMessage `json:"expected"`
	Tags             json.RawMessage `json:"tags"`
	Status           string          `json:"status"`
}

type UpdatePromptEvaluationCaseRequest struct {
	AssetID          *string         `json:"asset_id"`
	PromptID         json.RawMessage `json:"prompt_id"`
	CaseIndex        *int32          `json:"case_index"`
	CaseName         *string         `json:"case_name"`
	Variables        json.RawMessage `json:"variables"`
	ExpectedContains json.RawMessage `json:"expected_contains"`
	Input            json.RawMessage `json:"input"`
	Expected         json.RawMessage `json:"expected"`
	Tags             json.RawMessage `json:"tags"`
	Status           *string         `json:"status"`
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

type promptEvaluationAssetProfile struct {
	StructureSchema          string
	StructuredCaseCount      int32
	StructuredVariableCount  int32
	StructuredAssertionCount int32
	LinkedDatasetCount       int32
	LinkedPromptCount        int32
	EvaluationDimensionCount int32
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
	ID               string  `json:"id"`
	TaskID           string  `json:"task_id"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	EstimatedCost    float64 `json:"estimated_cost"`
	Priced           bool    `json:"priced"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type PromptEvaluationRunEvidenceResponse struct {
	Run          PromptEvaluationRunResponse         `json:"run"`
	Trials       []PromptEvaluationTrialResponse     `json:"trials"`
	TaskUsage    []PromptEvaluationTaskUsageResponse `json:"task_usage"`
	TaskMessages []protocol.TaskMessagePayload       `json:"task_messages"`
	TraceEvents  []TaskTraceEventResponse            `json:"trace_events"`
	Evidence     any                                 `json:"evidence"`
	Context      map[string]any                      `json:"上下文"`
}

type PromptEvaluationEvidenceSnapshotResponse struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	RunID         string  `json:"run_id"`
	SnapshotType  string  `json:"snapshot_type"`
	SchemaVersion string  `json:"schema_version"`
	Summary       any     `json:"summary"`
	Evidence      any     `json:"evidence,omitempty"`
	CreatedBy     *string `json:"created_by"`
	CreatedAt     string  `json:"created_at"`
}

type promptEvaluationEvidenceRefs struct {
	Asset   *db.PromptEvaluationAsset
	Prompt  *db.PromptLibraryItem
	Agent   *db.Agent
	Runtime *db.AgentRuntime
	Issue   *db.Issue
	Project *db.Project
	Squad   *db.Squad
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

type PromptEvaluationRuntimeReadinessResponse struct {
	Status             string                `json:"status"`
	Label              string                `json:"label"`
	Detail             string                `json:"detail"`
	Fix                string                `json:"fix"`
	Model              string                `json:"model"`
	Runtime            *AgentRuntimeResponse `json:"runtime"`
	LastSeenAgeSeconds int64                 `json:"last_seen_age_seconds"`
	CheckedAt          string                `json:"checked_at"`
}

type PromptEvaluationCaseResponse struct {
	ID               string                                  `json:"id"`
	WorkspaceID      string                                  `json:"workspace_id"`
	AssetID          string                                  `json:"asset_id"`
	PromptID         *string                                 `json:"prompt_id"`
	CaseIndex        int32                                   `json:"case_index"`
	CaseName         string                                  `json:"case_name"`
	Variables        any                                     `json:"variables"`
	ExpectedContains any                                     `json:"expected_contains"`
	Assertions       []PromptEvaluationCaseAssertionResponse `json:"assertions"`
	Input            any                                     `json:"input"`
	Expected         any                                     `json:"expected"`
	Tags             any                                     `json:"tags"`
	Status           string                                  `json:"status"`
	Source           string                                  `json:"source"`
	CreatedBy        *string                                 `json:"created_by"`
	CreatedAt        string                                  `json:"created_at"`
	UpdatedAt        string                                  `json:"updated_at"`
}

type PromptEvaluationCaseAssertionResponse struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	AssetID        string `json:"asset_id"`
	CaseID         string `json:"case_id"`
	AssertionIndex int32  `json:"assertion_index"`
	AssertionType  string `json:"assertion_type"`
	ExpectedText   string `json:"expected_text"`
	Status         string `json:"status"`
	Source         string `json:"source"`
	CreatedAt      string `json:"created_at"`
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

type UpdatePromptEvaluationOptimizationCandidateRequest struct {
	CandidateName    string `json:"candidate_name"`
	CandidateContent string `json:"candidate_content"`
	Rationale        string `json:"rationale"`
	EditNote         string `json:"edit_note"`
}

type RejectPromptEvaluationOptimizationCandidateRequest struct {
	Reason string `json:"reason"`
}

func promptEvaluationAssetToResponse(asset db.PromptEvaluationAsset) PromptEvaluationAssetResponse {
	return PromptEvaluationAssetResponse{
		ID:                       uuidToString(asset.ID),
		WorkspaceID:              uuidToString(asset.WorkspaceID),
		PromptID:                 uuidToPtr(asset.PromptID),
		Name:                     asset.Name,
		Description:              asset.Description,
		AssetType:                asset.AssetType,
		Payload:                  decodeJSONDefault(asset.Payload, map[string]any{}),
		Status:                   asset.Status,
		CreatedBy:                uuidToPtr(asset.CreatedBy),
		CreatedAt:                timestampToString(asset.CreatedAt),
		UpdatedAt:                timestampToString(asset.UpdatedAt),
		StructureSchema:          asset.StructureSchema,
		StructuredCaseCount:      asset.StructuredCaseCount,
		StructuredVariableCount:  asset.StructuredVariableCount,
		StructuredAssertionCount: asset.StructuredAssertionCount,
		LinkedDatasetCount:       asset.LinkedDatasetCount,
		LinkedPromptCount:        asset.LinkedPromptCount,
		EvaluationDimensionCount: asset.EvaluationDimensionCount,
		DatasetRowCount:          asset.DatasetRowCount,
		TestSuiteCaseCount:       asset.TestSuiteCaseCount,
	}
}

func promptEvaluationRunToResponse(run db.PromptEvaluationRun) PromptEvaluationRunResponse {
	return PromptEvaluationRunResponse{
		ID:                uuidToString(run.ID),
		WorkspaceID:       uuidToString(run.WorkspaceID),
		AssetID:           uuidToString(run.AssetID),
		PromptID:          uuidToPtr(run.PromptID),
		RunKind:           promptEvaluationRunKindLabel(run.RunKind),
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

func promptEvaluationRunKindLabel(runKind string) string {
	if runKind == "本地渲染" {
		return "模板渲染检查"
	}
	return runKind
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
	estimatedCost, priced := metrics.EstimateUsageCostUSD(usage.Model, usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
	return PromptEvaluationTaskUsageResponse{
		ID:               uuidToString(usage.ID),
		TaskID:           uuidToString(usage.TaskID),
		Provider:         usage.Provider,
		Model:            usage.Model,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		EstimatedCost:    estimatedCost,
		Priced:           priced,
		CreatedAt:        timestampToString(usage.CreatedAt),
		UpdatedAt:        timestampToString(usage.UpdatedAt),
	}
}

func promptEvaluationCaseToResponse(item db.PromptEvaluationCase, assertions []db.PromptEvaluationCaseAssertion) PromptEvaluationCaseResponse {
	return PromptEvaluationCaseResponse{
		ID:               uuidToString(item.ID),
		WorkspaceID:      uuidToString(item.WorkspaceID),
		AssetID:          uuidToString(item.AssetID),
		PromptID:         uuidToPtr(item.PromptID),
		CaseIndex:        item.CaseIndex,
		CaseName:         item.CaseName,
		Variables:        decodeJSONDefault(item.Variables, map[string]any{}),
		ExpectedContains: decodeJSONDefault(item.ExpectedContains, []any{}),
		Assertions:       promptEvaluationCaseAssertionsToResponse(assertions),
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

func promptEvaluationCaseAssertionsToResponse(assertions []db.PromptEvaluationCaseAssertion) []PromptEvaluationCaseAssertionResponse {
	resp := make([]PromptEvaluationCaseAssertionResponse, 0, len(assertions))
	for _, item := range assertions {
		resp = append(resp, PromptEvaluationCaseAssertionResponse{
			ID:             uuidToString(item.ID),
			WorkspaceID:    uuidToString(item.WorkspaceID),
			AssetID:        uuidToString(item.AssetID),
			CaseID:         uuidToString(item.CaseID),
			AssertionIndex: item.AssertionIndex,
			AssertionType:  item.AssertionType,
			ExpectedText:   item.ExpectedText,
			Status:         item.Status,
			Source:         item.Source,
			CreatedAt:      timestampToString(item.CreatedAt),
		})
	}
	return resp
}

func promptEvaluationAssertionsByCase(assertions []db.PromptEvaluationCaseAssertion) map[string][]db.PromptEvaluationCaseAssertion {
	grouped := make(map[string][]db.PromptEvaluationCaseAssertion)
	for _, item := range assertions {
		caseID := uuidToString(item.CaseID)
		grouped[caseID] = append(grouped[caseID], item)
	}
	return grouped
}

func promptEvaluationAssertionTexts(raw []byte) []string {
	values := stringListFromAny(decodeJSONDefault(raw, []any{}))
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func promptEvaluationExpectedContainsFromAssertions(fallback []byte, assertions []db.PromptEvaluationCaseAssertion) []any {
	if len(assertions) == 0 {
		if values, ok := decodeJSONDefault(fallback, []any{}).([]any); ok {
			return values
		}
		return []any{}
	}
	result := make([]any, 0, len(assertions))
	for _, item := range assertions {
		if strings.TrimSpace(item.ExpectedText) != "" {
			result = append(result, item.ExpectedText)
		}
	}
	return result
}

func syncPromptEvaluationCaseAssertions(ctx context.Context, qtx *db.Queries, item db.PromptEvaluationCase, expectedContains []byte) ([]db.PromptEvaluationCaseAssertion, error) {
	if err := qtx.DeletePromptEvaluationCaseAssertionsByCase(ctx, db.DeletePromptEvaluationCaseAssertionsByCaseParams{
		WorkspaceID: item.WorkspaceID,
		CaseID:      item.ID,
	}); err != nil {
		return nil, err
	}
	values := promptEvaluationAssertionTexts(expectedContains)
	assertions := make([]db.PromptEvaluationCaseAssertion, 0, len(values))
	for idx, value := range values {
		assertion, err := qtx.CreatePromptEvaluationCaseAssertion(ctx, db.CreatePromptEvaluationCaseAssertionParams{
			WorkspaceID:    item.WorkspaceID,
			AssetID:        item.AssetID,
			CaseID:         item.ID,
			AssertionIndex: int32(idx),
			ExpectedText:   value,
			AssertionType:  "包含文本",
			Status:         item.Status,
			Source:         "expected_contains",
		})
		if err != nil {
			return nil, err
		}
		assertions = append(assertions, assertion)
	}
	return assertions, nil
}

func syncPromptEvaluationDatasetRow(ctx context.Context, qtx *db.Queries, asset db.PromptEvaluationAsset, item db.PromptEvaluationCase) error {
	deletedAssets, err := qtx.DeletePromptEvaluationDatasetRowsByCase(ctx, db.DeletePromptEvaluationDatasetRowsByCaseParams{
		WorkspaceID: item.WorkspaceID,
		CaseID:      item.ID,
	})
	if err != nil {
		return err
	}
	for _, datasetAssetID := range deletedAssets {
		if err := refreshPromptEvaluationDatasetRowCount(ctx, qtx, item.WorkspaceID, datasetAssetID); err != nil {
			return err
		}
	}
	if asset.AssetType != promptEvaluationAssetDataset {
		return nil
	}
	if _, err := qtx.CreatePromptEvaluationDatasetRow(ctx, db.CreatePromptEvaluationDatasetRowParams{
		WorkspaceID:      item.WorkspaceID,
		DatasetAssetID:   item.AssetID,
		CaseID:           item.ID,
		RowIndex:         item.CaseIndex,
		RowName:          item.CaseName,
		Variables:        item.Variables,
		ExpectedContains: item.ExpectedContains,
		Expected:         item.Expected,
		Tags:             item.Tags,
		Status:           item.Status,
		Source:           item.Source,
		CreatedBy:        item.CreatedBy,
	}); err != nil {
		return err
	}
	return refreshPromptEvaluationDatasetRowCount(ctx, qtx, item.WorkspaceID, item.AssetID)
}

func syncPromptEvaluationTestSuiteCase(ctx context.Context, qtx *db.Queries, asset db.PromptEvaluationAsset, item db.PromptEvaluationCase) error {
	deletedAssets, err := qtx.DeletePromptEvaluationTestSuiteCasesByCase(ctx, db.DeletePromptEvaluationTestSuiteCasesByCaseParams{
		WorkspaceID: item.WorkspaceID,
		CaseID:      item.ID,
	})
	if err != nil {
		return err
	}
	for _, testSuiteAssetID := range deletedAssets {
		if err := refreshPromptEvaluationTestSuiteCaseCount(ctx, qtx, item.WorkspaceID, testSuiteAssetID); err != nil {
			return err
		}
	}
	if asset.AssetType != promptEvaluationAssetTestSuite {
		return nil
	}
	if _, err := qtx.CreatePromptEvaluationTestSuiteCase(ctx, db.CreatePromptEvaluationTestSuiteCaseParams{
		WorkspaceID:      item.WorkspaceID,
		TestSuiteAssetID: item.AssetID,
		CaseID:           item.ID,
		CaseIndex:        item.CaseIndex,
		CaseName:         item.CaseName,
		Variables:        item.Variables,
		ExpectedContains: item.ExpectedContains,
		Expected:         item.Expected,
		Tags:             item.Tags,
		Status:           item.Status,
		Source:           item.Source,
		CreatedBy:        item.CreatedBy,
	}); err != nil {
		return err
	}
	return refreshPromptEvaluationTestSuiteCaseCount(ctx, qtx, item.WorkspaceID, item.AssetID)
}

func deletePromptEvaluationDatasetRowsForCase(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, caseID pgtype.UUID) error {
	deletedAssets, err := qtx.DeletePromptEvaluationDatasetRowsByCase(ctx, db.DeletePromptEvaluationDatasetRowsByCaseParams{
		WorkspaceID: workspaceID,
		CaseID:      caseID,
	})
	if err != nil {
		return err
	}
	for _, datasetAssetID := range deletedAssets {
		if err := refreshPromptEvaluationDatasetRowCount(ctx, qtx, workspaceID, datasetAssetID); err != nil {
			return err
		}
	}
	return nil
}

func deletePromptEvaluationTestSuiteCasesForCase(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, caseID pgtype.UUID) error {
	deletedAssets, err := qtx.DeletePromptEvaluationTestSuiteCasesByCase(ctx, db.DeletePromptEvaluationTestSuiteCasesByCaseParams{
		WorkspaceID: workspaceID,
		CaseID:      caseID,
	})
	if err != nil {
		return err
	}
	for _, testSuiteAssetID := range deletedAssets {
		if err := refreshPromptEvaluationTestSuiteCaseCount(ctx, qtx, workspaceID, testSuiteAssetID); err != nil {
			return err
		}
	}
	return nil
}

func refreshPromptEvaluationDatasetRowCount(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, assetID pgtype.UUID) error {
	return qtx.RefreshPromptEvaluationDatasetRowCount(ctx, db.RefreshPromptEvaluationDatasetRowCountParams{
		WorkspaceID:    workspaceID,
		DatasetAssetID: assetID,
	})
}

func refreshPromptEvaluationTestSuiteCaseCount(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, assetID pgtype.UUID) error {
	return qtx.RefreshPromptEvaluationTestSuiteCaseCount(ctx, db.RefreshPromptEvaluationTestSuiteCaseCountParams{
		WorkspaceID:      workspaceID,
		TestSuiteAssetID: assetID,
	})
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

func promptEvaluationEvidenceSnapshotToResponse(item db.PromptEvaluationEvidenceSnapshot, includeEvidence bool) PromptEvaluationEvidenceSnapshotResponse {
	resp := PromptEvaluationEvidenceSnapshotResponse{
		ID:            uuidToString(item.ID),
		WorkspaceID:   uuidToString(item.WorkspaceID),
		RunID:         uuidToString(item.RunID),
		SnapshotType:  item.SnapshotType,
		SchemaVersion: item.SchemaVersion,
		Summary:       decodeJSONDefault(item.Summary, map[string]any{}),
		CreatedBy:     uuidToPtr(item.CreatedBy),
		CreatedAt:     timestampToString(item.CreatedAt),
	}
	if includeEvidence {
		resp.Evidence = decodeJSONDefault(item.Evidence, map[string]any{})
	}
	return resp
}

func promptEvaluationEvidenceSnapshotListRowToResponse(item db.ListPromptEvaluationEvidenceSnapshotsByRunRow) PromptEvaluationEvidenceSnapshotResponse {
	return PromptEvaluationEvidenceSnapshotResponse{
		ID:            uuidToString(item.ID),
		WorkspaceID:   uuidToString(item.WorkspaceID),
		RunID:         uuidToString(item.RunID),
		SnapshotType:  item.SnapshotType,
		SchemaVersion: item.SchemaVersion,
		Summary:       decodeJSONDefault(item.Summary, map[string]any{}),
		CreatedBy:     uuidToPtr(item.CreatedBy),
		CreatedAt:     timestampToString(item.CreatedAt),
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
			"模板渲染检查数":  row.LocalRuns,
			"需人工复核":    row.ReviewRuns,
			"待确认优化候选":  row.PendingCandidates,
			"已发布优化候选":  row.PublishedCandidates,
			"服务端证据快照":  row.EvidenceSnapshots,
			"验收归档快照":   row.AcceptanceSnapshots,
		},
		Assets: map[string]int64{
			"资产总数":    row.TotalAssets,
			"启用资产数":   row.ActiveAssets,
			"数据集":     row.DatasetAssets,
			"测试套件":    row.TestSuiteAssets,
			"实验":      row.ExperimentAssets,
			"优化运行":    row.OptimizationAssets,
			"结构化用例":   row.TotalCases,
			"启用用例":    row.ActiveCases,
			"画像用例数":   row.AssetProfileCases,
			"画像变量数":   row.AssetProfileVariables,
			"画像断言数":   row.AssetProfileAssertions,
			"关联数据集数":  row.AssetProfileLinkedDatasets,
			"关联提示词数":  row.AssetProfileLinkedPrompts,
			"评估维度数":   row.AssetProfileDimensions,
			"数据集行":    row.DatasetRows,
			"测试套件用例":  row.TestSuiteCases,
			"服务端证据快照": row.EvidenceSnapshots,
			"验收归档快照":  row.AcceptanceSnapshots,
		},
		RunStatus: map[string]int64{
			"运行总数":    row.TotalRuns,
			"模板渲染检查":  row.LocalRuns,
			"Agent执行": row.AgentRuns,
			"已入队":     row.QueuedRuns,
			"运行中":     row.RunningRuns,
			"通过":      row.PassedRuns,
			"未通过":     row.NotPassedRuns,
			"失败":      row.FailedRuns,
			"已取消":     row.CancelledRuns,
			"需人工复核":   row.ReviewRuns,
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

func promptEvaluationPayloadField(w http.ResponseWriter, raw json.RawMessage, field string, preserveEmpty bool) ([]byte, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if preserveEmpty {
			return nil, true
		}
		return mustJSONBytes(normalizePromptEvaluationPayloadObject(map[string]any{})), true
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		writeError(w, http.StatusBadRequest, field+" must be a JSON object")
		return nil, false
	}
	return mustJSONBytes(normalizePromptEvaluationPayloadObject(obj)), true
}

func promptEvaluationAssetProfileFromPayload(raw []byte, promptID pgtype.UUID) promptEvaluationAssetProfile {
	payload := decodePayloadObject(raw)
	cases := promptEvaluationCases(payload)
	variableCount := 0
	assertionCount := 0
	for index, item := range cases {
		normalized := normalizePromptEvaluationCase(index, item)
		variableCount += len(normalized.Variables)
		assertionCount += len(normalized.ExpectedContains)
	}
	linkedPromptCount := countPromptEvaluationProfileValues(payload, "prompt_ids", "提示词版本", "关联提示词", "候选提示词", "对比提示词", "baseline_prompt_id", "基线提示词")
	if promptID.Valid {
		linkedPromptCount++
	}
	return promptEvaluationAssetProfile{
		StructureSchema:          promptEvaluationAssetProfileV1,
		StructuredCaseCount:      int32(len(cases)),
		StructuredVariableCount:  int32(variableCount),
		StructuredAssertionCount: int32(assertionCount),
		LinkedDatasetCount:       int32(countPromptEvaluationProfileValues(payload, "dataset_ids", "数据集ID", "关联数据集", "包含数据集", "linked_dataset_ids")),
		LinkedPromptCount:        int32(linkedPromptCount),
		EvaluationDimensionCount: int32(countPromptEvaluationProfileValues(payload, "evaluation_dimensions", "评估维度", "指标", "指标口径", "metric_contract")),
	}
}

func countPromptEvaluationProfileValues(payload map[string]any, keys ...string) int {
	seen := map[string]bool{}
	for _, key := range keys {
		collectPromptEvaluationProfileValues(seen, firstValue(payload, key))
	}
	return len(seen)
}

func collectPromptEvaluationProfileValues(seen map[string]bool, value any) {
	switch v := value.(type) {
	case nil:
		return
	case string:
		if item := strings.TrimSpace(v); item != "" {
			seen[item] = true
		}
	case []any:
		for _, item := range v {
			collectPromptEvaluationProfileValues(seen, item)
		}
	case map[string]any:
		for key, item := range v {
			if strings.TrimSpace(key) != "" {
				seen[key] = true
			}
			collectPromptEvaluationProfileValues(seen, item)
		}
	default:
		if item := strings.TrimSpace(stringFromAny(v)); item != "" {
			seen[item] = true
		}
	}
}

func jsonObjectBytesOrDefault(w http.ResponseWriter, raw json.RawMessage, field string, fallback []byte) ([]byte, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fallback, true
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		writeError(w, http.StatusBadRequest, field+" must be a JSON object")
		return nil, false
	}
	return raw, true
}

func jsonArrayBytesOrDefault(w http.ResponseWriter, raw json.RawMessage, field string, fallback []byte) ([]byte, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fallback, true
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err != nil {
		writeError(w, http.StatusBadRequest, field+" must be a JSON array")
		return nil, false
	}
	return raw, true
}

func jsonObjectBytesForUpdate(w http.ResponseWriter, raw json.RawMessage, field string, existing []byte) ([]byte, bool) {
	return jsonObjectBytesOrDefault(w, raw, field, existing)
}

func jsonArrayBytesForUpdate(w http.ResponseWriter, raw json.RawMessage, field string, existing []byte) ([]byte, bool) {
	return jsonArrayBytesOrDefault(w, raw, field, existing)
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
	assertions, err := h.Queries.ListPromptEvaluationCaseAssertions(r.Context(), db.ListPromptEvaluationCaseAssertionsParams{
		WorkspaceID: workspaceUUID,
		AssetID:     assetID,
		Status:      status,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation case assertions")
		return
	}
	assertionsByCase := promptEvaluationAssertionsByCase(assertions)
	resp := make([]PromptEvaluationCaseResponse, len(cases))
	for i, item := range cases {
		resp[i] = promptEvaluationCaseToResponse(item, assertionsByCase[uuidToString(item.ID)])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) CreatePromptEvaluationCase(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreatePromptEvaluationCaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid prompt evaluation case payload")
		return
	}
	assetID, ok := parseUUIDOrBadRequest(w, req.AssetID, "asset_id")
	if !ok {
		return
	}
	asset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{ID: assetID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "asset_id does not belong to this workspace")
		return
	}
	promptID, ok := h.promptEvaluationPromptID(w, r, workspaceUUID, req.PromptID, asset.PromptID)
	if !ok {
		return
	}
	caseIndex := int32(0)
	if req.CaseIndex != nil {
		caseIndex = *req.CaseIndex
	} else {
		existing, err := h.Queries.ListPromptEvaluationCases(r.Context(), db.ListPromptEvaluationCasesParams{WorkspaceID: workspaceUUID, AssetID: asset.ID})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to allocate prompt evaluation case index")
			return
		}
		caseIndex = int32(len(existing))
	}
	if caseIndex < 0 {
		writeError(w, http.StatusBadRequest, "case_index must be greater than or equal to 0")
		return
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "启用"
	}
	if !validPromptLibraryStatus(status) {
		writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
		return
	}
	variables, ok := jsonObjectBytesOrDefault(w, req.Variables, "variables", []byte("{}"))
	if !ok {
		return
	}
	expectedContains, ok := jsonArrayBytesOrDefault(w, req.ExpectedContains, "expected_contains", []byte("[]"))
	if !ok {
		return
	}
	input, ok := jsonObjectBytesOrDefault(w, req.Input, "input", []byte("{}"))
	if !ok {
		return
	}
	expected, ok := jsonObjectBytesOrDefault(w, req.Expected, "expected", []byte("{}"))
	if !ok {
		return
	}
	tags, ok := jsonArrayBytesOrDefault(w, req.Tags, "tags", []byte("[]"))
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation case transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreatePromptEvaluationCase(r.Context(), db.CreatePromptEvaluationCaseParams{
		WorkspaceID:      workspaceUUID,
		AssetID:          asset.ID,
		PromptID:         promptID,
		CaseIndex:        caseIndex,
		CaseName:         strings.TrimSpace(req.CaseName),
		Variables:        variables,
		ExpectedContains: expectedContains,
		Input:            input,
		Expected:         expected,
		Tags:             tags,
		Status:           status,
		Source:           "manual",
		CreatedBy:        parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation case")
		return
	}
	assertions, err := syncPromptEvaluationCaseAssertions(r.Context(), qtx, created, expectedContains)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation case assertions")
		return
	}
	if err := syncPromptEvaluationDatasetRow(r.Context(), qtx, asset, created); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation dataset row")
		return
	}
	if err := syncPromptEvaluationTestSuiteCase(r.Context(), qtx, asset, created); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation test suite case")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation case")
		return
	}
	writeJSON(w, http.StatusCreated, promptEvaluationCaseToResponse(created, assertions))
}

func (h *Handler) UpdatePromptEvaluationCase(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	caseID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation case id")
	if !ok {
		return
	}
	current, err := h.Queries.GetPromptEvaluationCaseInWorkspace(r.Context(), db.GetPromptEvaluationCaseInWorkspaceParams{ID: caseID, WorkspaceID: workspaceUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation case not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation case")
		return
	}
	var req UpdatePromptEvaluationCaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid prompt evaluation case payload")
		return
	}
	assetID := current.AssetID
	if req.AssetID != nil {
		parsed, ok := parseUUIDOrBadRequest(w, *req.AssetID, "asset_id")
		if !ok {
			return
		}
		assetID = parsed
	}
	asset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{ID: assetID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "asset_id does not belong to this workspace")
		return
	}
	promptID, ok := h.promptEvaluationPromptID(w, r, workspaceUUID, req.PromptID, current.PromptID)
	if !ok {
		return
	}
	if len(req.PromptID) == 0 && !promptID.Valid {
		promptID = asset.PromptID
	}
	caseIndex := current.CaseIndex
	if req.CaseIndex != nil {
		caseIndex = *req.CaseIndex
	}
	if caseIndex < 0 {
		writeError(w, http.StatusBadRequest, "case_index must be greater than or equal to 0")
		return
	}
	caseName := current.CaseName
	if req.CaseName != nil {
		caseName = strings.TrimSpace(*req.CaseName)
	}
	status := current.Status
	if req.Status != nil {
		status = strings.TrimSpace(*req.Status)
	}
	if !validPromptLibraryStatus(status) {
		writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
		return
	}
	variables, ok := jsonObjectBytesForUpdate(w, req.Variables, "variables", current.Variables)
	if !ok {
		return
	}
	expectedContains, ok := jsonArrayBytesForUpdate(w, req.ExpectedContains, "expected_contains", current.ExpectedContains)
	if !ok {
		return
	}
	input, ok := jsonObjectBytesForUpdate(w, req.Input, "input", current.Input)
	if !ok {
		return
	}
	expected, ok := jsonObjectBytesForUpdate(w, req.Expected, "expected", current.Expected)
	if !ok {
		return
	}
	tags, ok := jsonArrayBytesForUpdate(w, req.Tags, "tags", current.Tags)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation case transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.UpdatePromptEvaluationCase(r.Context(), db.UpdatePromptEvaluationCaseParams{
		ID:               current.ID,
		WorkspaceID:      workspaceUUID,
		AssetID:          asset.ID,
		PromptID:         promptID,
		CaseIndex:        caseIndex,
		CaseName:         caseName,
		Variables:        variables,
		ExpectedContains: expectedContains,
		Input:            input,
		Expected:         expected,
		Tags:             tags,
		Status:           status,
		Source:           "manual",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update prompt evaluation case")
		return
	}
	assertions, err := syncPromptEvaluationCaseAssertions(r.Context(), qtx, updated, expectedContains)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update prompt evaluation case assertions")
		return
	}
	if err := syncPromptEvaluationDatasetRow(r.Context(), qtx, asset, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation dataset row")
		return
	}
	if err := syncPromptEvaluationTestSuiteCase(r.Context(), qtx, asset, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation test suite case")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation case")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationCaseToResponse(updated, assertions))
}

func (h *Handler) DeletePromptEvaluationCase(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	caseID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation case id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetPromptEvaluationCaseInWorkspace(r.Context(), db.GetPromptEvaluationCaseInWorkspaceParams{ID: caseID, WorkspaceID: workspaceUUID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation case not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation case")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation case transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := deletePromptEvaluationDatasetRowsForCase(r.Context(), qtx, workspaceUUID, caseID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete prompt evaluation dataset row")
		return
	}
	if err := deletePromptEvaluationTestSuiteCasesForCase(r.Context(), qtx, workspaceUUID, caseID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete prompt evaluation test suite case")
		return
	}
	if err := qtx.DeletePromptEvaluationCase(r.Context(), db.DeletePromptEvaluationCaseParams{ID: caseID, WorkspaceID: workspaceUUID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete prompt evaluation case")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation case deletion")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	runs, err := h.Queries.ListPromptEvaluationRuns(r.Context(), db.ListPromptEvaluationRunsParams{
		WorkspaceID: workspaceUUID,
		AssetID:     assetID,
		Status:      status,
		Since:       since,
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

	return PromptEvaluationRunEvidenceResponse{
		Run:          promptEvaluationRunToResponse(run),
		Trials:       trialResp,
		TaskUsage:    usageResp,
		TaskMessages: messageResp,
		TraceEvents:  traceResp,
		Evidence:     decodeJSONDefault(run.Evidence, map[string]any{}),
		Context:      buildPromptEvaluationEvidenceContext(run, task, refs, trialResp, usageResp, messageResp, traceResp),
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
	evidence, err := h.buildPromptEvaluationRunEvidenceResponse(r.Context(), workspaceUUID, runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to build prompt evaluation evidence snapshot")
		return
	}
	now := time.Now().UTC()
	payload := map[string]any{
		"语义版本": "multica.prompt_evaluation.evidence_snapshot.v1",
		"生成时间": now.Format(time.RFC3339),
		"快照类型": snapshotType,
		"运行证据": evidence,
	}
	item, err := h.Queries.CreatePromptEvaluationEvidenceSnapshot(r.Context(), db.CreatePromptEvaluationEvidenceSnapshotParams{
		WorkspaceID:   workspaceUUID,
		RunID:         runID,
		SnapshotType:  snapshotType,
		SchemaVersion: "multica.prompt_evaluation.evidence_snapshot.v1",
		Summary:       mustJSONBytes(buildPromptEvaluationEvidenceSnapshotSummary(evidence, now)),
		Evidence:      mustJSONBytes(payload),
		CreatedBy:     parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation evidence snapshot")
		return
	}
	writeJSON(w, http.StatusCreated, promptEvaluationEvidenceSnapshotToResponse(item, true))
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

func buildPromptEvaluationEvidenceSnapshotSummary(evidence PromptEvaluationRunEvidenceResponse, generatedAt time.Time) map[string]any {
	run := evidence.Run
	return map[string]any{
		"语义版本":          "multica.prompt_evaluation.evidence_snapshot.summary.v1",
		"生成时间":          generatedAt.Format(time.RFC3339),
		"运行ID":          run.ID,
		"运行类型":          run.RunKind,
		"运行状态":          run.Status,
		"触发来源":          run.TriggerSource,
		"总用例数":          run.TotalCases,
		"通过数":           run.PassedCases,
		"失败数":           run.FailedCases,
		"通过率":           run.PassRate,
		"总耗时毫秒":         run.TotalDurationMs,
		"输入token":       run.InputTokens,
		"输出token":       run.OutputTokens,
		"预估成本":          run.EstimatedCost,
		"执行Agent":       run.AgentID,
		"模型":            run.Model,
		"runtime":       run.RuntimeID,
		"runtime供应商":    run.RuntimeProvider,
		"trace/task id": run.TaskID,
		"失败原因":          promptEvaluationEvidenceFailureReason(evidence),
		"评估结论":          run.Conclusion,
		"trial数":        len(evidence.Trials),
		"usage行数":       len(evidence.TaskUsage),
		"任务消息数":         len(evidence.TaskMessages),
		"trace事件数":      len(evidence.TraceEvents),
		"上下文字段数":        len(evidence.Context),
	}
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
		sourceSummary["生成说明"] = "基于结构化运行记录、失败用例和真实 Agent task 证据生成优化候选；候选不会自动替换生产提示词。"
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

func (h *Handler) RunPromptEvaluationOptimizationAgent(w http.ResponseWriter, r *http.Request) {
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
	sourceRun, err := h.Queries.GetPromptEvaluationRunInWorkspace(r.Context(), db.GetPromptEvaluationRunInWorkspaceParams{ID: runID, WorkspaceID: workspaceUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation run")
		return
	}
	if !sourceRun.PromptID.Valid {
		writeError(w, http.StatusBadRequest, "prompt_id is required to create an optimization agent run")
		return
	}
	if !promptEvaluationRunHasFailure(sourceRun) {
		writeError(w, http.StatusBadRequest, "only failed or not-passed runs can create an optimization agent run")
		return
	}
	prompt, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{ID: sourceRun.PromptID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "prompt_id does not belong to this workspace")
		return
	}
	trials, err := h.Queries.ListPromptEvaluationTrialsByRun(r.Context(), db.ListPromptEvaluationTrialsByRunParams{RunID: sourceRun.ID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load failed prompt evaluation trials")
		return
	}
	sourceSummary := buildPromptEvaluationCandidateFailureSummary(sourceRun, trials)
	runtimeEvidence, err := h.promptEvaluationCandidateRuntimeEvidence(r.Context(), sourceRun)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation runtime evidence")
		return
	}
	if runtimeEvidence != nil {
		sourceSummary["真实Agent运行证据"] = runtimeEvidence
		sourceSummary["生成说明"] = "基于结构化运行记录、失败用例和真实 Agent task 证据创建优化 Agent 任务；输出不会自动替换生产提示词。"
	}
	member, ok := h.workspaceMember(w, r, uuidToString(workspaceUUID))
	if !ok {
		return
	}
	agentRow, runtimeRow, ok := h.ensurePromptEvaluationAgent(w, r, workspaceUUID, parseUUID(userID), member)
	if !ok {
		return
	}
	payload := buildPromptEvaluationOptimizationAgentPayload(prompt, sourceRun, sourceSummary)
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode optimization agent payload")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start optimization agent transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	asset, err := qtx.CreatePromptEvaluationAsset(r.Context(), db.CreatePromptEvaluationAssetParams{
		WorkspaceID: workspaceUUID,
		PromptID:    sourceRun.PromptID,
		Name:        buildPromptEvaluationOptimizationAgentAssetName(prompt, sourceRun),
		Description: "由失败运行创建的真实 Agent 优化任务，输出用于人工确认后的提示词候选。",
		AssetType:   promptEvaluationAssetOptimize,
		Payload:     payloadBytes,
		Status:      "启用",
		CreatedBy:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create optimization agent asset")
		return
	}
	if ok := h.syncPromptEvaluationCasesFromPayload(w, r, qtx, asset, parseUUID(userID)); !ok {
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit optimization agent asset")
		return
	}

	session, err := h.Queries.CreateChatSession(r.Context(), db.CreateChatSessionParams{
		WorkspaceID: asset.WorkspaceID,
		AgentID:     agentRow.ID,
		CreatorID:   parseUUID(userID),
		Title:       "训练评估优化：" + prompt.Name,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create optimization agent chat session")
		return
	}
	cases, ok := h.promptEvaluationCasesForAsset(w, r, asset)
	if !ok {
		return
	}
	messageText := buildPromptEvaluationAgentMessage(asset, prompt, promptEvaluationPayloadWithCases(payload, cases))
	msg, err := h.Queries.CreateChatMessage(r.Context(), db.CreateChatMessageParams{
		ChatSessionID: session.ID,
		Role:          "user",
		Content:       messageText,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create optimization agent chat message")
		return
	}
	task, err := h.TaskService.EnqueueChatTask(r.Context(), session, parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue optimization agent task: "+err.Error())
		return
	}
	if err := h.Queries.LinkChatMessageToTask(r.Context(), db.LinkChatMessageToTaskParams{ID: msg.ID, TaskID: task.ID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link optimization agent message to task")
		return
	}
	run, ok := h.persistPromptEvaluationQueuedAgentRun(w, r, asset, agentRow, runtimeRow, task.ID, session.ID, parseUUID(userID), "优化运行", payload, cases)
	if !ok {
		return
	}
	runIndex := map[string]any{
		"运行时间":            time.Now().UTC().Format(time.RFC3339),
		"run_id":          uuidToString(run.ID),
		"source_run_id":   uuidToString(sourceRun.ID),
		"状态":              "已入队",
		"执行Agent":         agentRow.Name,
		"agent_id":        uuidToString(agentRow.ID),
		"模型":              promptEvaluationAgentModel(),
		"runtime":         runtimeRow.Provider,
		"runtime_id":      uuidToString(runtimeRow.ID),
		"trace/task id":   uuidToString(task.ID),
		"chat_session_id": uuidToString(session.ID),
		"失败原因":            "无",
		"评估结论":            "等待 Agent 生成优化建议，人工确认后才能发布新提示词版本",
	}
	payload["最近Agent运行"] = runIndex
	payload["Agent运行记录"] = appendPromptEvaluationAgentRunHistory(payload["Agent运行记录"], runIndex)
	updated, err := h.Queries.UpdatePromptEvaluationAsset(r.Context(), db.UpdatePromptEvaluationAssetParams{
		ID:          asset.ID,
		WorkspaceID: asset.WorkspaceID,
		PromptID:    asset.PromptID,
		Payload:     mustJSONBytes(payload),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save optimization agent run")
		return
	}
	writeJSON(w, http.StatusAccepted, PromptEvaluationAgentRunResponse{
		Asset:         promptEvaluationAssetToResponse(updated),
		Run:           promptEvaluationRunToResponse(run),
		TaskID:        uuidToString(task.ID),
		ChatSessionID: uuidToString(session.ID),
		AgentID:       uuidToString(agentRow.ID),
		RuntimeID:     uuidToString(runtimeRow.ID),
		Model:         promptEvaluationAgentModel(),
		Status:        "已入队",
		Message:       "真实 Agent 优化任务已入队；完成后可在运行历史查看证据，再生成人工确认候选。",
	})
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
		estimatedCost, priced := metrics.EstimateUsageCostUSD(
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
			"estimated_cost":     metrics.RoundCostUSD(estimatedCost),
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
	if stringFromAny(payload["任务类型"]) != "Agent 优化运行" {
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
	sourceSummary["生成说明"] = "由 Agent 优化运行输出自动生成优化候选；候选不会自动替换生产提示词，必须人工确认后发布。"
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
	if _, err := createPromptLibraryVersion(r.Context(), qtx, publishedPrompt, promptLibraryVersionSourceOptimization, candidate.ID); err != nil {
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
	payload, ok := promptEvaluationPayloadField(w, req.Payload, "payload", false)
	if !ok {
		return
	}
	profile := promptEvaluationAssetProfileFromPayload(payload, promptID)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	asset, err := qtx.CreatePromptEvaluationAsset(r.Context(), db.CreatePromptEvaluationAssetParams{
		WorkspaceID:              workspaceUUID,
		Name:                     req.Name,
		Description:              req.Description,
		AssetType:                req.AssetType,
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
	createdAsset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{ID: asset.ID, WorkspaceID: asset.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload prompt evaluation asset")
		return
	}
	writeJSON(w, http.StatusCreated, promptEvaluationAssetToResponse(createdAsset))
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
	payload, ok := promptEvaluationPayloadField(w, req.Payload, "payload", true)
	if !ok {
		return
	}
	var profile *promptEvaluationAssetProfile
	if payload != nil {
		next := promptEvaluationAssetProfileFromPayload(payload, promptID)
		profile = &next
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	assetParams := db.UpdatePromptEvaluationAssetParams{
		ID:          existing.ID,
		WorkspaceID: existing.WorkspaceID,
		PromptID:    promptID,
		Name:        textParam(req.Name),
		Description: textParam(req.Description),
		AssetType:   textParam(req.AssetType),
		Payload:     payload,
		Status:      textParam(req.Status),
	}
	if profile != nil {
		assetParams.StructureSchema = pgtype.Text{String: profile.StructureSchema, Valid: true}
		assetParams.StructuredCaseCount = pgtype.Int4{Int32: profile.StructuredCaseCount, Valid: true}
		assetParams.StructuredVariableCount = pgtype.Int4{Int32: profile.StructuredVariableCount, Valid: true}
		assetParams.StructuredAssertionCount = pgtype.Int4{Int32: profile.StructuredAssertionCount, Valid: true}
		assetParams.LinkedDatasetCount = pgtype.Int4{Int32: profile.LinkedDatasetCount, Valid: true}
		assetParams.LinkedPromptCount = pgtype.Int4{Int32: profile.LinkedPromptCount, Valid: true}
		assetParams.EvaluationDimensionCount = pgtype.Int4{Int32: profile.EvaluationDimensionCount, Valid: true}
	}
	asset, err := qtx.UpdatePromptEvaluationAsset(r.Context(), assetParams)
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
	updatedAsset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{ID: asset.ID, WorkspaceID: asset.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload prompt evaluation asset")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationAssetToResponse(updatedAsset))
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
	payload := decodePayloadObject(asset.Payload)
	cases, ok := h.promptEvaluationCasesForAsset(w, r, asset)
	if !ok {
		return
	}
	messageText := buildPromptEvaluationAgentMessage(asset, prompt, promptEvaluationPayloadWithCases(payload, cases))
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

	run, ok := h.persistPromptEvaluationQueuedAgentRun(w, r, asset, agentRow, runtimeRow, task.ID, session.ID, parseUUID(userID), "Agent 调试场", payload, cases)
	if !ok {
		return
	}
	runIndex := map[string]any{
		"运行时间":            time.Now().UTC().Format(time.RFC3339),
		"run_id":          uuidToString(run.ID),
		"状态":              "已入队",
		"执行Agent":         agentRow.Name,
		"agent_id":        uuidToString(agentRow.ID),
		"模型":              promptEvaluationAgentModel(),
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
		Model:         promptEvaluationAgentModel(),
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

func (h *Handler) persistPromptEvaluationQueuedAgentRun(w http.ResponseWriter, r *http.Request, asset db.PromptEvaluationAsset, agent db.Agent, runtime db.AgentRuntime, taskID pgtype.UUID, chatSessionID pgtype.UUID, createdBy pgtype.UUID, triggerSource string, payload map[string]any, cases []map[string]any) (db.PromptEvaluationRun, bool) {
	run, err := h.Queries.CreatePromptEvaluationRun(r.Context(), db.CreatePromptEvaluationRunParams{
		WorkspaceID:       asset.WorkspaceID,
		AssetID:           asset.ID,
		PromptID:          asset.PromptID,
		RunKind:           "Agent执行",
		Status:            "已入队",
		TriggerSource:     triggerSource,
		AgentID:           agent.ID,
		RuntimeID:         runtime.ID,
		TaskID:            taskID,
		ChatSessionID:     chatSessionID,
		Model:             promptEvaluationAgentModel(),
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
			"模型":            promptEvaluationAgentModel(),
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
	assertions, err := h.Queries.ListPromptEvaluationCaseAssertions(r.Context(), db.ListPromptEvaluationCaseAssertionsParams{
		WorkspaceID: asset.WorkspaceID,
		AssetID:     asset.ID,
		Status:      pgtype.Text{String: "启用", Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation case assertions")
		return nil, false
	}
	assertionsByCase := promptEvaluationAssertionsByCase(assertions)
	cases := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		cases = append(cases, map[string]any{
			"名称":   row.CaseName,
			"变量":   decodeJSONDefault(row.Variables, map[string]any{}),
			"期望包含": promptEvaluationExpectedContainsFromAssertions(row.ExpectedContains, assertionsByCase[uuidToString(row.ID)]),
			"输入":   decodeJSONDefault(row.Input, map[string]any{}),
			"期望":   decodeJSONDefault(row.Expected, map[string]any{}),
		})
	}
	return cases, true
}

func promptEvaluationPassRate(passed int32, failed int32) float64 {
	total := passed + failed
	if total == 0 {
		return 0
	}
	return float64(passed) / float64(total)
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
	if _, ok := sourceSummary["真实Agent运行证据"].(map[string]any); ok {
		rationale = "基于失败用例和真实 Agent task 证据补充中文输出约束、失败处理要求、证据字段和验收口径；原提示词不被自动替换，必须人工确认后发布。"
	}
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
	if runtimeEvidence, ok := sourceSummary["真实Agent运行证据"].(map[string]any); ok {
		lines = append(lines, "", "真实Agent输出摘要：")
		if messages, ok := runtimeEvidence["task消息"].([]map[string]any); ok && len(messages) > 0 {
			for _, message := range messages {
				content := strings.TrimSpace(stringFromAny(message["content"]))
				if content == "" {
					content = strings.TrimSpace(stringFromAny(message["output"]))
				}
				if content == "" {
					continue
				}
				lines = append(lines, "- task消息 #"+stringFromAny(message["seq"])+"："+truncatePromptEvaluationEvidence(content, 240))
			}
		}
		if traces, ok := runtimeEvidence["trace事件"].([]map[string]any); ok && len(traces) > 0 {
			for _, trace := range traces {
				name := stringFromAny(firstValue(trace, "event_name", "event_type"))
				status := stringFromAny(trace["status"])
				reason := stringFromAny(trace["failure_reason"])
				if name == "" {
					name = "未命名 trace"
				}
				if reason == "" {
					reason = "无"
				}
				lines = append(lines, "- trace "+name+"："+status+"，失败原因："+reason)
			}
		}
		if usages, ok := runtimeEvidence["task用量"].([]map[string]any); ok && len(usages) > 0 {
			for _, usage := range usages {
				lines = append(lines, "- 用量 "+stringFromAny(usage["provider"])+"/"+stringFromAny(usage["model"])+"：输入 "+stringFromAny(usage["input_tokens"])+"，输出 "+stringFromAny(usage["output_tokens"])+"，预估成本 "+stringFromAny(usage["estimated_cost"]))
			}
		}
	}
	lines = append(lines, "", "人工发布要求：发布前必须由验收者确认该候选不会降低原有通过用例质量。")
	return strings.Join(lines, "\n"), rationale
}

type promptEvaluationAgentOptimizationOutput struct {
	Name      string
	Content   string
	Rationale string
	Impact    string
	Checklist []string
	Raw       string
}

func buildPromptEvaluationAgentOptimizationCandidateContent(prompt db.PromptLibraryItem, sourceRun db.PromptEvaluationRun, agentRun db.PromptEvaluationRun, sourceSummary map[string]any) (string, string) {
	output := parsePromptEvaluationAgentOptimizationOutput(sourceSummary)
	failureReason := strings.TrimSpace(sourceRun.FailureReason)
	if failureReason == "" {
		failureReason = "来源失败运行需要优化提示词约束。"
	}
	candidateContent := strings.TrimSpace(output.Content)
	if candidateContent == "" {
		candidateContent = strings.Join([]string{
			strings.TrimSpace(prompt.Content),
			"",
			"【Agent 优化候选】",
			"来源失败运行：" + uuidToString(sourceRun.ID),
			"来源 Agent 优化运行：" + uuidToString(agentRun.ID),
			"失败原因：" + failureReason,
			"",
			"Agent 优化输出摘要：",
			truncatePromptEvaluationEvidence(firstNonEmptyPromptEvaluationString(output.Raw, output.Rationale, "Agent 未返回结构化候选正文，已基于运行证据生成待确认候选。"), 1200),
			"",
			"请在后续执行中严格遵守：",
			"1. 全部输出使用中文，明确需求边界、影响范围和验收条件。",
			"2. 输出必须包含可观测证据：耗时、执行 Agent、模型、runtime、trace/task id、失败原因和评估结论。",
			"3. 对失败场景给出缺失字段、失败用例、修复建议和下一步人工确认点。",
			"4. 不要自动替换生产提示词；发布前必须由验收者确认。",
		}, "\n")
	}
	lines := []string{
		candidateContent,
		"",
		"【Agent 优化运行证据】",
		"来源失败运行：" + uuidToString(sourceRun.ID),
		"来源 Agent 优化运行：" + uuidToString(agentRun.ID),
		"失败原因：" + failureReason,
	}
	if output.Rationale != "" {
		lines = append(lines, "逐条修改依据："+output.Rationale)
	}
	if output.Impact != "" {
		lines = append(lines, "可能影响的通过用例："+output.Impact)
	}
	if len(output.Checklist) > 0 {
		lines = append(lines, "人工验收清单：")
		for _, item := range output.Checklist {
			lines = append(lines, "- "+item)
		}
	}
	lines = append(lines, "人工发布要求：发布前必须由验收者确认该候选不会降低原有通过用例质量。")
	rationale := "由真实 Agent 优化运行输出自动生成候选；原提示词不被自动替换，必须人工确认后发布。"
	if output.Rationale != "" {
		rationale = "由真实 Agent 优化运行输出自动生成候选：" + truncatePromptEvaluationEvidence(output.Rationale, 240)
	}
	return strings.Join(lines, "\n"), rationale
}

func parsePromptEvaluationAgentOptimizationOutput(sourceSummary map[string]any) promptEvaluationAgentOptimizationOutput {
	runtimeEvidence, _ := sourceSummary["真实Agent优化运行证据"].(map[string]any)
	var rawParts []string
	parseMessage := func(message map[string]any) (promptEvaluationAgentOptimizationOutput, bool) {
		for _, key := range []string{"content", "output"} {
			raw := strings.TrimSpace(stringFromAny(message[key]))
			if raw == "" {
				continue
			}
			rawParts = append(rawParts, raw)
			for _, parsed := range promptEvaluationJSONValues(raw) {
				if output := promptEvaluationAgentOptimizationOutputFromJSON(parsed); output.Content != "" || output.Rationale != "" {
					output.Raw = raw
					return output, true
				}
			}
		}
		return promptEvaluationAgentOptimizationOutput{}, false
	}
	if messages, ok := runtimeEvidence["task消息"].([]map[string]any); ok {
		for _, message := range messages {
			if output, ok := parseMessage(message); ok {
				return output
			}
		}
	}
	if messages, ok := runtimeEvidence["task消息"].([]any); ok {
		for _, value := range messages {
			message, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if output, ok := parseMessage(message); ok {
				return output
			}
		}
	}
	return promptEvaluationAgentOptimizationOutput{Raw: truncatePromptEvaluationEvidence(strings.Join(rawParts, "\n\n"), 2000)}
}

func promptEvaluationAgentOptimizationOutputFromJSON(value any) promptEvaluationAgentOptimizationOutput {
	row, ok := value.(map[string]any)
	if !ok {
		return promptEvaluationAgentOptimizationOutput{}
	}
	checklist := stringListFromAny(firstValue(row, "人工验收清单", "验收清单", "checklist", "acceptance_checklist"))
	return promptEvaluationAgentOptimizationOutput{
		Name:      firstNonEmptyPromptEvaluationField(row, "优化候选名称", "候选名称", "candidate_name", "name"),
		Content:   firstNonEmptyPromptEvaluationField(row, "候选提示词正文", "候选提示词", "候选内容", "candidate_content", "content", "prompt"),
		Rationale: firstNonEmptyPromptEvaluationField(row, "逐条修改依据", "修改依据", "rationale", "reasoning"),
		Impact:    firstNonEmptyPromptEvaluationField(row, "可能影响的通过用例", "影响面", "impact"),
		Checklist: checklist,
	}
}

func promptEvaluationJSONValues(source string) []any {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil
	}
	candidates := []string{source}
	parts := strings.Split(source, "```")
	for index := 1; index < len(parts); index += 2 {
		block := strings.TrimSpace(parts[index])
		if newline := strings.IndexByte(block, '\n'); newline >= 0 {
			header := strings.TrimSpace(strings.ToLower(block[:newline]))
			if header == "json" || header == "javascript" || header == "js" {
				block = strings.TrimSpace(block[newline+1:])
			}
		}
		candidates = append(candidates, block)
	}
	if start, end := strings.Index(source, "{"), strings.LastIndex(source, "}"); start >= 0 && end > start {
		candidates = append(candidates, source[start:end+1])
	}
	values := make([]any, 0, len(candidates))
	for _, candidate := range candidates {
		var value any
		if err := json.Unmarshal([]byte(strings.TrimSpace(candidate)), &value); err == nil {
			values = append(values, value)
		}
	}
	return values
}

func buildPromptEvaluationOptimizationAgentPayload(prompt db.PromptLibraryItem, run db.PromptEvaluationRun, sourceSummary map[string]any) map[string]any {
	failedCases, _ := sourceSummary["失败用例"].([]map[string]any)
	cases := make([]map[string]any, 0, len(failedCases))
	for index, failedCase := range failedCases {
		name := stringFromAny(failedCase["用例名称"])
		if name == "" {
			name = "失败用例 " + strconv.Itoa(index+1)
		}
		expected := firstValue(failedCase, "期望", "期望包含")
		cases = append(cases, map[string]any{
			"名称":   name,
			"变量":   firstValue(failedCase, "输入", "变量"),
			"期望包含": expected,
			"失败原因": firstValue(failedCase, "失败原因", "状态"),
		})
	}
	if len(cases) == 0 {
		cases = append(cases, map[string]any{
			"名称":   "失败运行整体优化",
			"变量":   map[string]any{"source_run_id": uuidToString(run.ID)},
			"期望包含": []string{"优化候选", "失败原因", "验收条件", "trace/task id"},
			"失败原因": run.FailureReason,
		})
	}
	return map[string]any{
		"schema_version": 1,
		"语义版本":           "multica.training_evaluation.optimization_agent.v1",
		"任务类型":           "Agent 优化运行",
		"来源运行":           uuidToString(run.ID),
		"来源资产":           uuidToString(run.AssetID),
		"来源提示词":          uuidToString(prompt.ID),
		"提示词名称":          prompt.Name,
		"原始提示词内容":        prompt.Content,
		"失败摘要":           sourceSummary,
		"cases":          cases,
		"优化目标": []string{
			"基于失败用例和真实 Agent task 证据生成候选提示词正文。",
			"候选必须继续保持中文语义、可观测字段、验收条件和失败处理要求。",
			"不要自动发布；输出必须便于验收者人工确认后再发布。",
		},
		"输出格式": []string{
			"优化候选名称",
			"候选提示词正文",
			"逐条修改依据",
			"可能影响的通过用例",
			"人工验收清单",
		},
	}
}

func buildPromptEvaluationOptimizationAgentAssetName(prompt db.PromptLibraryItem, run db.PromptEvaluationRun) string {
	return prompt.Name + " Agent 优化运行 " + run.CreatedAt.Time.Format("20060102") + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
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

func truncatePromptEvaluationEvidence(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
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
			existing.Model.String == promptEvaluationAgentModel() &&
			existing.Instructions == instructions {
			return existing, runtime, true
		}
		updated, err := h.Queries.UpdateAgent(r.Context(), db.UpdateAgentParams{
			ID:           existing.ID,
			RuntimeMode:  pgtype.Text{String: runtime.RuntimeMode, Valid: true},
			RuntimeID:    runtime.ID,
			Instructions: pgtype.Text{String: instructions, Valid: true},
			Model:        pgtype.Text{String: promptEvaluationAgentModel(), Valid: true},
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
		Model:              pgtype.Text{String: promptEvaluationAgentModel(), Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create training evaluation agent")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	return created, runtime, true
}

func (h *Handler) selectPromptEvaluationRuntime(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, member db.Member) (db.AgentRuntime, bool) {
	readiness, err := h.promptEvaluationRuntimeReadiness(r.Context(), workspaceID, member)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes for training evaluation")
		return db.AgentRuntime{}, false
	}
	if readiness.Status == "就绪" && readiness.Runtime != nil {
		runtimeID := parseUUID(readiness.Runtime.ID)
		runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
			ID:          runtimeID,
			WorkspaceID: workspaceID,
		})
		if err == nil {
			return runtime, true
		}
	}
	writeError(w, http.StatusServiceUnavailable, readiness.Fix)
	return db.AgentRuntime{}, false
}

func (h *Handler) promptEvaluationRuntimeReadiness(ctx context.Context, workspaceID pgtype.UUID, member db.Member) (PromptEvaluationRuntimeReadinessResponse, error) {
	checkedAt := time.Now().UTC()
	runtimes, err := h.Queries.ListAgentRuntimes(ctx, workspaceID)
	if err != nil {
		return PromptEvaluationRuntimeReadinessResponse{}, err
	}
	provider := promptEvaluationAgentProvider()
	providerName := strings.ToUpper(provider[:1]) + provider[1:]
	inaccessibleRuntime := 0
	var best *db.AgentRuntime
	for i := range runtimes {
		runtime := runtimes[i]
		if !strings.EqualFold(runtime.Provider, provider) {
			continue
		}
		if !canUseRuntimeForAgent(member, runtime) {
			inaccessibleRuntime++
			continue
		}
		if best == nil || runtimeReadinessRank(runtime, checkedAt) > runtimeReadinessRank(*best, checkedAt) {
			best = &runtime
		}
	}
	if best == nil {
		if inaccessibleRuntime > 0 {
			return promptEvaluationRuntimeReadinessResponse("无权限", providerName+" 无权限", "当前 workspace 存在 "+providerName+" runtime，但你没有绑定或使用权限。", "请让 runtime 所有者将 "+providerName+" runtime 设为 public，或由 workspace 管理员为训练评估 Agent 绑定可用 runtime。", nil, checkedAt), nil
		}
		return promptEvaluationRuntimeReadinessResponse("缺失", providerName+" 缺失", "当前 workspace 未发现 "+providerName+" runtime，Agent 调试场不能执行 "+promptEvaluationAgentModel()+"。", "安装并配置 "+provider+"，启动 multica daemon，等待 /api/runtimes 出现 provider="+provider+" 且 status=online 的 runtime。", nil, checkedAt), nil
	}
	ageSeconds := promptEvaluationRuntimeAgeSeconds(*best, checkedAt)
	respRuntime := runtimeToResponse(*best)
	if best.Status != "online" {
		return promptEvaluationRuntimeReadinessResponse("离线", providerName+" 离线", "已注册 "+providerName+" runtime「"+best.Name+"」，但当前状态是离线，不能创建真实 Agent 任务。", "启动 multica daemon，并确认 "+provider+" 可执行文件在 PATH 中，或设置对应 MULTICA_<PROVIDER>_PATH 后重启 daemon。", &respRuntime, checkedAt), nil
	}
	if !best.LastSeenAt.Valid || checkedAt.Sub(best.LastSeenAt.Time) > promptEvaluationRuntimeFreshTTL {
		return promptEvaluationRuntimeReadinessResponse("过期", providerName+" 心跳过期", providerName+" runtime「"+best.Name+"」状态仍是 online，但最近心跳已经超过 2 分钟，不能证明当前可执行。", "检查 multica daemon 是否仍在运行，确认网络和心跳正常后等待 last_seen_at 刷新，再创建真实 Agent 任务。", &respRuntime, checkedAt), nil
	}
	recentCapacityFailure, err := h.Queries.GetRecentRuntimeCapacityFailure(ctx, db.GetRecentRuntimeCapacityFailureParams{
		WorkspaceID: workspaceID,
		RuntimeID:   best.ID,
		CompletedAt: pgtype.Timestamptz{Time: checkedAt.Add(-promptEvaluationRuntimeLimitTTL), Valid: true},
		Model:       pgtype.Text{String: promptEvaluationAgentModel(), Valid: true},
	})
	if err == nil {
		detail := providerName + " runtime「" + best.Name + "」在线且心跳新鲜，但最近任务 " + uuidToString(recentCapacityFailure.ID) + " 返回模型容量或额度限制，当前不能证明 " + promptEvaluationAgentModel() + " 可执行。"
		if recentCapacityFailure.Error.Valid && strings.TrimSpace(recentCapacityFailure.Error.String) != "" {
			detail += " 最近错误：" + truncatePromptEvaluationEvidence(recentCapacityFailure.Error.String, 180)
		}
		resp := promptEvaluationRuntimeReadinessResponse("容量受限", "模型额度受限", detail, "优先切换到 "+fallbackPromptEvaluationAgentModel+"；如果持续出现 429/529，请申请模型额度或让管理员调整 Agent 模型配置。", &respRuntime, checkedAt)
		resp.LastSeenAgeSeconds = ageSeconds
		return resp, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PromptEvaluationRuntimeReadinessResponse{}, err
	}
	resp := promptEvaluationRuntimeReadinessResponse("就绪", providerName+" 在线", "已发现在线且心跳新鲜的 "+providerName+" runtime「"+best.Name+"」，可以作为 "+promptEvaluationAgentModel()+" 的真实执行目标。", "无需修复；下一步应创建真实 Agent 任务并采集 trace、token、成本和输出。", &respRuntime, checkedAt)
	resp.LastSeenAgeSeconds = ageSeconds
	return resp, nil
}

func promptEvaluationRuntimeReadinessResponse(status, label, detail, fix string, runtime *AgentRuntimeResponse, checkedAt time.Time) PromptEvaluationRuntimeReadinessResponse {
	ageSeconds := int64(-1)
	if runtime != nil && runtime.LastSeenAt != nil {
		if lastSeen, err := time.Parse(time.RFC3339, *runtime.LastSeenAt); err == nil {
			ageSeconds = int64(checkedAt.Sub(lastSeen).Seconds())
		}
	}
	return PromptEvaluationRuntimeReadinessResponse{
		Status:             status,
		Label:              label,
		Detail:             detail,
		Fix:                fix,
		Model:              promptEvaluationAgentModel(),
		Runtime:            runtime,
		LastSeenAgeSeconds: ageSeconds,
		CheckedAt:          checkedAt.Format(time.RFC3339),
	}
}

func runtimeReadinessRank(runtime db.AgentRuntime, now time.Time) int {
	if runtime.Status == "online" && runtime.LastSeenAt.Valid && now.Sub(runtime.LastSeenAt.Time) <= promptEvaluationRuntimeFreshTTL {
		return 3
	}
	if runtime.Status == "online" {
		return 2
	}
	return 1
}

func promptEvaluationRuntimeAgeSeconds(runtime db.AgentRuntime, now time.Time) int64 {
	if !runtime.LastSeenAt.Valid {
		return -1
	}
	return int64(now.Sub(runtime.LastSeenAt.Time).Seconds())
}

func promptEvaluationAgentInstructions() string {
	return strings.Join([]string{
		"你是 Multica 训练与评估 Agent。你只负责执行当前提示词评估任务，必须使用中文输出。",
		"输出必须包含执行结论、逐用例结果、失败原因、改进建议、可复盘证据。",
		"最终回复必须包含一个可机读 JSON 代码块，schema 必须是 multica.training_evaluation.agent_verdict.v1，字段为：schema_version、schema、case_results、summary。",
		"不要修改业务代码，不要创建 issue，不要泄露密钥。",
	}, "\n")
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
		"3. 给出中文指标：总用例数、通过数、失败数、通过率、总耗时、平均耗时、输入 token、输出 token、预估成本、执行 Agent、模型、运行时、trace/任务标识、失败原因、评估结论。",
		"4. 如果需要优化提示词，只输出候选建议，不要自动替换生产版本。",
		"5. 最终必须附带一个 JSON 代码块，Multica 会用它自动回写运行历史；缺失该结构会被标记为“需人工复核”。",
		"",
		"【必须返回的 JSON schema】",
		"```json",
		`{
	  "schema_version": 1,
	  "schema": "multica.training_evaluation.agent_verdict.v1",
	  "case_results": [
    {
      "case_index": 0,
      "status": "通过 | 未通过 | 需人工复核",
      "output": "该用例的实际输出或摘要",
      "failure_reason": "无 | 失败原因 | 需复核原因",
      "evidence": {
        "命中": ["命中的期望字段"],
        "缺失": ["缺失的期望字段"],
        "trace_task_id": "如已知则填写"
      }
    }
  ],
	  "summary": {
	    "total_cases": 0,
	    "passed_cases": 0,
	    "failed_cases": 0,
	    "failure_reason": "无 | 汇总失败原因",
	    "conclusion": "中文结论",
	    "improvement_suggestions": ["中文建议"],
	    "reproducible_evidence": ["任务标识、日志、关键输出摘要"]
	  }
	}`,
		"```",
	}, "\n")
}

func promptEvaluationPayloadWithCases(payload map[string]any, cases []map[string]any) map[string]any {
	result := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		result[key] = value
	}
	result["cases"] = cases
	result["用例"] = cases
	return result
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
		Model:             "本地模板渲染检查",
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

func normalizePromptEvaluationPayloadObject(payload map[string]any) map[string]any {
	normalized := make(map[string]any, len(payload)+4)
	for key, value := range payload {
		normalized[key] = value
	}
	normalized["schema_version"] = 1
	normalized["schema"] = "multica.training_evaluation.payload.v1"
	normalized["语义版本"] = "multica.training_evaluation.v1"
	normalized["payload_contract"] = map[string]any{
		"schema_version": 1,
		"schema":         "multica.training_evaluation.payload.v1",
		"cases":          "cases[].case_name / variables / expected_contains / tags",
		"兼容读取":           []string{"数据集", "用例", "测试用例", "test_cases", "training_cases", "evaluation_cases", "用例集"},
		"写入策略":           "新建和更新统一写入规范 cases；旧字段仅作为兼容迁移来源保留。",
	}
	cases := promptEvaluationCases(payload)
	normalizedCases := make([]map[string]any, 0, len(cases))
	for index, item := range cases {
		c := normalizePromptEvaluationCase(index, item)
		expectedContains := c.ExpectedContains
		if expectedContains == nil {
			expectedContains = []string{}
		}
		tags := c.Tags
		if tags == nil {
			tags = []string{}
		}
		normalizedCases = append(normalizedCases, map[string]any{
			"case_name":         c.Name,
			"variables":         c.Variables,
			"expected_contains": expectedContains,
			"tags":              tags,
		})
	}
	normalized["cases"] = normalizedCases
	return normalized
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
	runID := stringFromAny(result["run_id"])
	next := []any{result}
	for _, item := range history {
		if runID != "" {
			if existing, ok := item.(map[string]any); ok && stringFromAny(existing["run_id"]) == runID {
				continue
			}
		}
		next = append(next, item)
	}
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
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.Itoa(int(v))
	case int64:
		return strconv.FormatInt(v, 10)
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

func firstNonEmptyPromptEvaluationString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonEmptyPromptEvaluationField(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(stringFromAny(row[key])); value != "" {
			return value
		}
	}
	return ""
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
