package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
	promptEvaluationDatasetExportV1      = "multica.prompt_evaluation.dataset_export.v1"
	promptEvaluationDatasetImportV1      = "multica.prompt_evaluation.dataset_import.v1"
	promptEvaluationAgentName            = "Multica 训练评估智能体"
	legacyPromptEvaluationAgentName      = "Multica 训练评估 Agent"
	defaultPromptEvaluationAgentProvider = "codebuddy"
	defaultPromptEvaluationAgentModel    = "deepseek-v4-pro-ioa"
	fallbackPromptEvaluationAgentModel   = "deepseek-v4-pro-ioa"
	promptEvaluationRuntimeFreshTTL      = 2 * time.Minute
	promptEvaluationRuntimeLimitTTL      = 10 * time.Minute
)

var (
	promptTemplateVariablePattern           = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)
	errPromptEvaluationDatasetVersionNoRows = errors.New("dataset version requires at least one enabled row")
)

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
	ExperimentDimensionCount int32   `json:"experiment_dimension_count"`
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

type PromptEvaluationDatasetExportResponse struct {
	Schema        string                         `json:"schema"`
	ExportedAt    string                         `json:"exported_at"`
	SourceAssetID string                         `json:"source_asset_id"`
	Asset         PromptEvaluationAssetResponse  `json:"asset"`
	CaseCount     int                            `json:"case_count"`
	Cases         []PromptEvaluationCaseResponse `json:"cases"`
	Payload       map[string]any                 `json:"payload"`
}

type ImportPromptEvaluationDatasetRequest struct {
	Name        string                                `json:"name"`
	Description string                                `json:"description"`
	PromptID    json.RawMessage                       `json:"prompt_id"`
	Status      string                                `json:"status"`
	Export      PromptEvaluationDatasetExportResponse `json:"export"`
}

type ImportPromptEvaluationDatasetResponse struct {
	Asset         PromptEvaluationAssetResponse  `json:"asset"`
	SourceAssetID string                         `json:"source_asset_id"`
	CaseCount     int                            `json:"case_count"`
	Cases         []PromptEvaluationCaseResponse `json:"cases"`
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

type BulkPromptEvaluationCaseTagsRequest struct {
	AssetID       string   `json:"asset_id"`
	Source        string   `json:"source"`
	Tag           string   `json:"tag"`
	Keyword       string   `json:"keyword"`
	Status        string   `json:"status"`
	Tags          []string `json:"tags"`
	SourceTag     string   `json:"source_tag"`
	TargetTag     string   `json:"target_tag"`
	Mode          string   `json:"mode"`
	ExecutionMode string   `json:"execution_mode"`
	Limit         int32    `json:"limit"`
}

type CreatePromptEvaluationDatasetFromTracesRequest struct {
	TaskIDs          []string `json:"task_ids"`
	EventType        string   `json:"event_type"`
	Limit            int32    `json:"limit"`
	ExpectedContains []string `json:"expected_contains"`
	Tags             []string `json:"tags"`
}

type CreatePromptEvaluationDatasetVersionRequest struct {
	VersionLabel string          `json:"version_label"`
	Metadata     json.RawMessage `json:"metadata"`
}

type RestorePromptEvaluationDatasetVersionRequest struct {
	VersionLabel string          `json:"version_label"`
	Metadata     json.RawMessage `json:"metadata"`
}

type PromptEvaluationDatasetVersionResponse struct {
	ID             string  `json:"id"`
	WorkspaceID    string  `json:"workspace_id"`
	DatasetAssetID string  `json:"dataset_asset_id"`
	Version        int32   `json:"version"`
	VersionLabel   string  `json:"version_label"`
	RowCount       int32   `json:"row_count"`
	RowFingerprint string  `json:"row_fingerprint"`
	Metadata       any     `json:"metadata"`
	CreatedBy      *string `json:"created_by"`
	CreatedAt      string  `json:"created_at"`
}

type PromptEvaluationDatasetVersionRowResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	DatasetVersionID string  `json:"dataset_version_id"`
	DatasetAssetID   string  `json:"dataset_asset_id"`
	SourceRowID      *string `json:"source_row_id"`
	CaseID           *string `json:"case_id"`
	RowIndex         int32   `json:"row_index"`
	RowName          string  `json:"row_name"`
	Variables        any     `json:"variables"`
	ExpectedContains any     `json:"expected_contains"`
	Expected         any     `json:"expected"`
	Tags             any     `json:"tags"`
	Source           string  `json:"source"`
	CreatedAt        string  `json:"created_at"`
}

type PromptEvaluationDatasetVersionTagTrendResponse struct {
	DatasetVersionID string `json:"dataset_version_id"`
	Version          int32  `json:"version"`
	VersionLabel     string `json:"version_label"`
	CreatedAt        string `json:"created_at"`
	Tag              string `json:"tag"`
	CaseCount        int32  `json:"case_count"`
}

type PromptEvaluationDatasetVersionDiffResponse struct {
	BaseVersion   PromptEvaluationDatasetVersionResponse      `json:"base_version"`
	TargetVersion PromptEvaluationDatasetVersionResponse      `json:"target_version"`
	Summary       map[string]int                              `json:"summary"`
	Added         []PromptEvaluationDatasetVersionRowResponse `json:"added"`
	Removed       []PromptEvaluationDatasetVersionRowResponse `json:"removed"`
	Changed       []PromptEvaluationDatasetVersionChangedRow  `json:"changed"`
	Unchanged     []PromptEvaluationDatasetVersionRowResponse `json:"unchanged"`
}

type PromptEvaluationDatasetVersionChangedRow struct {
	RowIndex int32                                     `json:"row_index"`
	Base     PromptEvaluationDatasetVersionRowResponse `json:"base"`
	Target   PromptEvaluationDatasetVersionRowResponse `json:"target"`
}

type RestorePromptEvaluationDatasetVersionResponse struct {
	Asset           PromptEvaluationAssetResponse          `json:"asset"`
	RestoredFrom    PromptEvaluationDatasetVersionResponse `json:"restored_from"`
	RestoredVersion PromptEvaluationDatasetVersionResponse `json:"restored_version"`
	RestoredCases   []PromptEvaluationCaseResponse         `json:"restored_cases"`
}

type promptEvaluationRunResult struct {
	RunAt             string                                     `json:"运行时间"`
	AssetType         string                                     `json:"资产类型"`
	PromptName        string                                     `json:"提示词"`
	PromptVersion     int32                                      `json:"提示词版本"`
	TotalCases        int                                        `json:"总用例数"`
	PassedCases       int                                        `json:"通过用例数"`
	FailedCases       int                                        `json:"失败用例数"`
	PassRate          float64                                    `json:"通过率"`
	TotalDurationMs   int64                                      `json:"总耗时毫秒"`
	AverageDurationMs int64                                      `json:"平均耗时毫秒"`
	InputTokens       int                                        `json:"输入token"`
	OutputTokens      int                                        `json:"输出token"`
	EstimatedCost     float64                                    `json:"预估成本"`
	AgentName         string                                     `json:"执行Agent"`
	Model             string                                     `json:"模型"`
	Runtime           string                                     `json:"runtime"`
	TraceTaskID       string                                     `json:"trace/task id"`
	FailureReason     string                                     `json:"失败原因"`
	Conclusion        string                                     `json:"评估结论"`
	MissingVarCount   int                                        `json:"缺失变量数"`
	CaseResults       []promptEvaluationCaseRunResult            `json:"用例结果"`
	DimensionScores   []promptEvaluationExperimentDimensionScore `json:"实验维度评分"`
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

type promptEvaluationExperimentDimensionScore struct {
	DimensionIndex int32   `json:"维度序号"`
	DimensionName  string  `json:"维度名称"`
	Score          float64 `json:"得分"`
	PassedCases    int     `json:"通过用例数"`
	TotalCases     int     `json:"总用例数"`
	Status         string  `json:"状态"`
	Rule           string  `json:"评分规则"`
	Evidence       string  `json:"证据"`
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
	ExperimentDimensionCount int32
}

type normalizedPromptEvaluationExperimentDimension struct {
	Name              string
	ExperimentTarget  string
	BaselineOutput    string
	ComparisonPayload map[string]any
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
	ReviewDecision    string  `json:"review_decision"`
	ReviewNote        string  `json:"review_note"`
	ReviewedBy        *string `json:"reviewed_by"`
	ReviewedAt        string  `json:"reviewed_at"`
}

type ReviewPromptEvaluationRunRequest struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
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

type PromptEvaluationExecutionSpanResponse struct {
	ID         string         `json:"id"`
	ParentID   string         `json:"parent_id,omitempty"`
	SpanKind   string         `json:"span_kind"`
	SpanName   string         `json:"span_name"`
	Status     string         `json:"status"`
	Seq        int            `json:"seq"`
	TaskID     string         `json:"task_id,omitempty"`
	Tool       string         `json:"tool,omitempty"`
	Provider   string         `json:"provider,omitempty"`
	Model      string         `json:"model,omitempty"`
	TokenTotal int64          `json:"token_total"`
	DurationMs int64          `json:"duration_ms"`
	Summary    string         `json:"summary"`
	Details    map[string]any `json:"details,omitempty"`
	CreatedAt  string         `json:"created_at,omitempty"`
}

type PromptEvaluationToolCallChainResponse struct {
	ID             string         `json:"id"`
	TaskID         string         `json:"task_id,omitempty"`
	Tool           string         `json:"tool,omitempty"`
	Status         string         `json:"status"`
	UseSeq         int            `json:"use_seq,omitempty"`
	ResultSeq      int            `json:"result_seq,omitempty"`
	UseSpanID      string         `json:"use_span_id,omitempty"`
	ResultSpanID   string         `json:"result_span_id,omitempty"`
	Input          map[string]any `json:"input,omitempty"`
	Output         string         `json:"output,omitempty"`
	DurationMs     int64          `json:"duration_ms,omitempty"`
	ResultCategory string         `json:"result_category,omitempty"`
	FailureSignal  bool           `json:"failure_signal"`
	FailureReason  string         `json:"failure_reason,omitempty"`
	Summary        string         `json:"summary"`
	CreatedAt      string         `json:"created_at,omitempty"`
	CompletedAt    string         `json:"completed_at,omitempty"`
}

type PromptEvaluationToolCallSummaryResponse struct {
	Tool                   string         `json:"tool"`
	TotalCalls             int            `json:"total_calls"`
	PairedCalls            int            `json:"paired_calls"`
	MissingResultCalls     int            `json:"missing_result_calls"`
	OrphanResultCalls      int            `json:"orphan_result_calls"`
	AverageDurationMs      int64          `json:"average_duration_ms,omitempty"`
	MaxDurationMs          int64          `json:"max_duration_ms,omitempty"`
	SlowestToolCallChainID string         `json:"slowest_tool_call_chain_id,omitempty"`
	ResultCategories       map[string]int `json:"result_categories,omitempty"`
	FailureSignalCalls     int            `json:"failure_signal_calls"`
	NeedsAttention         bool           `json:"needs_attention"`
	Summary                string         `json:"summary"`
}

type PromptEvaluationRunEvidenceResponse struct {
	Run              PromptEvaluationRunResponse               `json:"run"`
	Trials           []PromptEvaluationTrialResponse           `json:"trials"`
	TaskUsage        []PromptEvaluationTaskUsageResponse       `json:"task_usage"`
	TaskMessages     []protocol.TaskMessagePayload             `json:"task_messages"`
	TraceEvents      []TaskTraceEventResponse                  `json:"trace_events"`
	ExecutionSpans   []PromptEvaluationExecutionSpanResponse   `json:"execution_spans"`
	ToolCallChains   []PromptEvaluationToolCallChainResponse   `json:"tool_call_chains"`
	ToolCallSummary  []PromptEvaluationToolCallSummaryResponse `json:"tool_call_summary"`
	ExecutionSummary map[string]any                            `json:"execution_summary"`
	Evidence         any                                       `json:"evidence"`
	Context          map[string]any                            `json:"上下文"`
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

type PromptEvaluationAssetEvidenceSnapshotResponse struct {
	AssetID      string                                      `json:"asset_id"`
	SnapshotType string                                      `json:"snapshot_type"`
	TotalRuns    int                                         `json:"total_runs"`
	CreatedCount int                                         `json:"created_count"`
	SkippedCount int                                         `json:"skipped_count"`
	Items        []PromptEvaluationEvidenceSnapshotResponse  `json:"items"`
	Skipped      []PromptEvaluationAssetEvidenceSnapshotSkip `json:"skipped"`
}

type PromptEvaluationAssetEvidenceArchiveItem struct {
	Run       PromptEvaluationRunResponse                `json:"run"`
	Snapshots []PromptEvaluationEvidenceSnapshotResponse `json:"snapshots"`
}

type PromptEvaluationAssetEvidenceArchivePackage struct {
	SchemaVersion    string                                     `json:"schema_version"`
	GeneratedAt      string                                     `json:"generated_at"`
	AssetID          string                                     `json:"asset_id"`
	SnapshotType     string                                     `json:"snapshot_type"`
	TotalRuns        int                                        `json:"total_runs"`
	ArchivedRunCount int                                        `json:"archived_run_count"`
	MissingRunCount  int                                        `json:"missing_run_count"`
	Asset            PromptEvaluationAssetResponse              `json:"asset"`
	Items            []PromptEvaluationAssetEvidenceArchiveItem `json:"items"`
	ChineseSummary   map[string]any                             `json:"中文摘要"`
}

type PromptEvaluationAssetEvidenceSnapshotSkip struct {
	RunID  string `json:"run_id"`
	Reason string `json:"reason"`
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

type PromptEvaluationCaseOperationResponse struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	AssetID       string  `json:"asset_id"`
	OperationType string  `json:"operation_type"`
	Filter        any     `json:"filter"`
	Input         any     `json:"input"`
	ChangedCount  int32   `json:"changed_count"`
	SkippedCount  int32   `json:"skipped_count"`
	SampleCaseIDs any     `json:"sample_case_ids"`
	CreatedBy     *string `json:"created_by"`
	CreatedAt     string  `json:"created_at"`
	Status        string  `json:"status"`
	ErrorMessage  string  `json:"error_message"`
	StartedAt     *string `json:"started_at"`
	CompletedAt   *string `json:"completed_at"`
	UpdatedAt     string  `json:"updated_at"`
}

type BulkPromptEvaluationCaseTagsResponse struct {
	Operation    PromptEvaluationCaseOperationResponse `json:"operation"`
	Cases        []PromptEvaluationCaseResponse        `json:"cases"`
	ChangedCount int32                                 `json:"changed_count"`
	SkippedCount int32                                 `json:"skipped_count"`
}

type promptEvaluationCaseBulkTagsJob struct {
	WorkspaceID   pgtype.UUID
	Asset         db.PromptEvaluationAsset
	CreatedBy     pgtype.UUID
	Source        pgtype.Text
	Status        pgtype.Text
	Tag           pgtype.Text
	Keyword       pgtype.Text
	Limit         int32
	Mode          string
	TargetTags    []string
	SourceTag     string
	TargetTag     string
	OperationType string
	FilterPayload map[string]any
	InputPayload  map[string]any
}

type promptEvaluationCaseBulkTagsResult struct {
	Operation    db.PromptEvaluationCaseOperation
	ChangedCases []db.PromptEvaluationCase
	ChangedCount int32
	SkippedCount int32
}

type PromptEvaluationCaseTagSummaryResponse struct {
	Tag       string `json:"tag"`
	CaseCount int32  `json:"case_count"`
}

type PromptEvaluationCaseTagDatasetSummaryDatasetResponse struct {
	AssetID   string `json:"asset_id"`
	AssetName string `json:"asset_name"`
	CaseCount int32  `json:"case_count"`
}

type PromptEvaluationCaseTagDatasetSummaryResponse struct {
	Tag          string                                                 `json:"tag"`
	CaseCount    int32                                                  `json:"case_count"`
	DatasetCount int32                                                  `json:"dataset_count"`
	TopDatasets  []PromptEvaluationCaseTagDatasetSummaryDatasetResponse `json:"top_datasets"`
}

type promptEvaluationCaseCursor struct {
	Offset        int32  `json:"offset"`
	SortBy        string `json:"sort_by"`
	SortDirection string `json:"sort_direction"`
	LastID        string `json:"last_id"`
	CaseIndex     int32  `json:"case_index"`
	CaseName      string `json:"case_name,omitempty"`
	Source        string `json:"source,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
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

type PromptEvaluationDatasetFromTracesResponse struct {
	Asset        PromptEvaluationAssetResponse  `json:"asset"`
	Cases        []PromptEvaluationCaseResponse `json:"cases"`
	TraceEvents  []TaskTraceEventResponse       `json:"trace_events"`
	CreatedCount int                            `json:"created_count"`
	SkippedCount int                            `json:"skipped_count"`
	Source       string                         `json:"source"`
}

type PromptEvaluationExperimentDimensionResponse struct {
	ID                string  `json:"id"`
	WorkspaceID       string  `json:"workspace_id"`
	ExperimentAssetID string  `json:"experiment_asset_id"`
	DimensionIndex    int32   `json:"dimension_index"`
	DimensionName     string  `json:"dimension_name"`
	ExperimentTarget  string  `json:"experiment_target"`
	BaselineOutput    string  `json:"baseline_output"`
	ComparisonPayload any     `json:"comparison_payload"`
	Status            string  `json:"status"`
	Source            string  `json:"source"`
	CreatedBy         *string `json:"created_by"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

type PromptEvaluationDimensionScoreResponse struct {
	ID             string  `json:"id"`
	WorkspaceID    string  `json:"workspace_id"`
	RunID          string  `json:"run_id"`
	AssetID        string  `json:"asset_id"`
	PromptID       *string `json:"prompt_id"`
	DimensionIndex int32   `json:"dimension_index"`
	DimensionName  string  `json:"dimension_name"`
	Score          float64 `json:"score"`
	PassedCases    int32   `json:"passed_cases"`
	TotalCases     int32   `json:"total_cases"`
	Status         string  `json:"status"`
	Rule           string  `json:"rule"`
	Evidence       string  `json:"evidence"`
	Source         string  `json:"source"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

type PromptEvaluationDimensionScoreSummaryResponse struct {
	WorkspaceID    string  `json:"workspace_id"`
	AssetID        string  `json:"asset_id"`
	PromptID       *string `json:"prompt_id"`
	DimensionIndex int32   `json:"dimension_index"`
	DimensionName  string  `json:"dimension_name"`
	RunCount       int64   `json:"run_count"`
	ScoredRunCount int64   `json:"scored_run_count"`
	PassedCases    int64   `json:"passed_cases"`
	TotalCases     int64   `json:"total_cases"`
	Score          float64 `json:"score"`
	LatestStatus   string  `json:"latest_status"`
	LatestRule     string  `json:"latest_rule"`
	LatestEvidence string  `json:"latest_evidence"`
	LatestSource   string  `json:"latest_source"`
	LatestScoredAt string  `json:"latest_scored_at"`
}

type PromptEvaluationDimensionScoreTrendResponse struct {
	WorkspaceID    string  `json:"workspace_id"`
	AssetID        string  `json:"asset_id"`
	PromptID       *string `json:"prompt_id"`
	DimensionIndex int32   `json:"dimension_index"`
	DimensionName  string  `json:"dimension_name"`
	Period         string  `json:"period"`
	PromptVersion  int32   `json:"prompt_version"`
	RunCount       int64   `json:"run_count"`
	ScoredRunCount int64   `json:"scored_run_count"`
	PassedCases    int64   `json:"passed_cases"`
	TotalCases     int64   `json:"total_cases"`
	Score          float64 `json:"score"`
	LatestStatus   string  `json:"latest_status"`
	LatestRule     string  `json:"latest_rule"`
	LatestEvidence string  `json:"latest_evidence"`
	LatestSource   string  `json:"latest_source"`
	LatestScoredAt string  `json:"latest_scored_at"`
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
	SkillPatch           any     `json:"skill_patch,omitempty"`
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
	CandidateName    string                      `json:"candidate_name"`
	CandidateContent string                      `json:"candidate_content"`
	Rationale        string                      `json:"rationale"`
	EditNote         string                      `json:"edit_note"`
	SkillPatch       *PromptEvaluationSkillPatch `json:"skill_patch,omitempty"`
}

type RejectPromptEvaluationOptimizationCandidateRequest struct {
	Reason string `json:"reason"`
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
			writeError(w, http.StatusBadRequest, "asset_type must be 数据集 or 测试套件")
			return
		}
		assetType = pgtype.Text{String: value, Valid: true}
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptEvaluationCaseStatus(value) {
			writeError(w, http.StatusBadRequest, promptEvaluationCaseStatusError())
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
		writeError(w, http.StatusBadRequest, "asset_type must be 数据集 or 测试套件")
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
	profile := promptEvaluationAssetProfileFromPayload(payload, promptID, req.AssetType)
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
		ExperimentDimensionCount: profile.ExperimentDimensionCount,
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
	if err := syncPromptEvaluationExperimentDimensions(r.Context(), qtx, asset, parseUUID(userID)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation experiment dimensions")
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
		writeError(w, http.StatusBadRequest, "asset_type must be 数据集 or 测试套件")
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
		next := promptEvaluationAssetProfileFromPayload(payload, promptID, existing.AssetType)
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
		assetParams.ExperimentDimensionCount = pgtype.Int4{Int32: profile.ExperimentDimensionCount, Valid: true}
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
	if err := syncPromptEvaluationExperimentDimensions(r.Context(), qtx, asset, existing.CreatedBy); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation experiment dimensions")
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
	payload := decodePayloadObject(asset.Payload)
	agentRow, runtimeRow, ok := h.selectPromptEvaluationExecutionAgent(w, r, asset.WorkspaceID, parseUUID(userID), member, payload)
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

	triggerSource := "评测运行"
	if asset.AssetType == promptEvaluationAssetOptimize {
		if promptEvaluationOptimizationRoundCount(payload) > 0 {
			triggerSource = "优化运行重试"
		} else {
			triggerSource = "优化运行"
		}
	}
	run, ok := h.persistPromptEvaluationQueuedAgentRun(w, r, asset, prompt, agentRow, runtimeRow, task.ID, session.ID, parseUUID(userID), triggerSource, payload, cases)
	if !ok {
		return
	}
	optimizationRound := 0
	optimizationRetry := 0
	if asset.AssetType == promptEvaluationAssetOptimize {
		existingRounds := promptEvaluationOptimizationRoundCount(payload)
		optimizationRound = existingRounds + 1
		if existingRounds > 0 {
			optimizationRetry = promptEvaluationOptimizationRetryCount(payload) + 1
		}
	}
	runIndex := map[string]any{
		"运行时间":            time.Now().UTC().Format(time.RFC3339),
		"run_id":          uuidToString(run.ID),
		"状态":              "已入队",
		"执行Agent":         agentRow.Name,
		"agent_id":        uuidToString(agentRow.ID),
		"模型":              promptEvaluationModelForAgent(agentRow),
		"runtime":         runtimeRow.Provider,
		"runtime_id":      uuidToString(runtimeRow.ID),
		"trace/task id":   uuidToString(task.ID),
		"chat_session_id": uuidToString(session.ID),
		"失败原因":            "无",
		"评估结论":            "等待智能体执行完成",
	}
	if asset.AssetType == promptEvaluationAssetOptimize {
		runIndex["轮次"] = optimizationRound
		runIndex["重试序号"] = optimizationRetry
	}
	payload["最近Agent运行"] = runIndex
	payload["Agent运行记录"] = appendPromptEvaluationAgentRunHistory(payload["Agent运行记录"], runIndex)
	if asset.AssetType == promptEvaluationAssetOptimize {
		sourceRunID := stringFromAny(payload["来源运行"])
		eventName := "创建优化运行"
		if optimizationRetry > 0 {
			eventName = "重试优化运行"
		}
		applyPromptEvaluationOptimizationRunContract(payload, uuidToString(asset.ID), sourceRunID, runIndex, eventName)
	}
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
		Model:         promptEvaluationModelForAgent(agentRow),
		Status:        "已入队",
		Message:       promptEvaluationAgentRunMessage(asset.AssetType),
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
		"提示词版本":   result.PromptVersion,
		"实验维度评分":  result.DimensionScores,
		"失败原因":    result.FailureReason,
		"评估结论":    result.Conclusion,
	}
	datasetVersionBindings, ok := h.promptEvaluationDatasetVersionBindings(w, r, asset.WorkspaceID, decodePayloadObject(asset.Payload))
	if !ok {
		return db.PromptEvaluationRun{}, false
	}
	if len(datasetVersionBindings) > 0 {
		metrics["数据集版本数"] = len(datasetVersionBindings)
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
		Evidence:          mustJSONBytes(map[string]any{"资产类型": asset.AssetType, "运行方式": "本地提示词渲染", "提示词版本": result.PromptVersion, "数据集版本": datasetVersionBindings, "实验维度评分": result.DimensionScores}),
		StartedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		CompletedAt:       pgtype.Timestamptz{Time: now, Valid: true},
		CreatedBy:         createdBy,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation run")
		return db.PromptEvaluationRun{}, false
	}
	if err := h.persistPromptEvaluationDimensionScores(r.Context(), run, result.DimensionScores, "local_run"); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist prompt evaluation dimension scores")
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

func (h *Handler) persistPromptEvaluationQueuedAgentRun(w http.ResponseWriter, r *http.Request, asset db.PromptEvaluationAsset, prompt db.PromptLibraryItem, agent db.Agent, runtime db.AgentRuntime, taskID pgtype.UUID, chatSessionID pgtype.UUID, createdBy pgtype.UUID, triggerSource string, payload map[string]any, cases []map[string]any) (db.PromptEvaluationRun, bool) {
	datasetVersionBindings, ok := h.promptEvaluationDatasetVersionBindings(w, r, asset.WorkspaceID, payload)
	if !ok {
		return db.PromptEvaluationRun{}, false
	}
	dimensionScores := pendingPromptEvaluationExperimentDimensionScores(promptEvaluationExperimentDimensionsForAsset(asset.AssetType, payload), len(cases))
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
		Model:             promptEvaluationModelForAgent(agent),
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
		Conclusion:        "等待智能体执行完成",
		Metrics: mustJSONBytes(map[string]any{
			"总用例数":          len(cases),
			"通过数":           0,
			"失败数":           0,
			"通过率":           0,
			"执行Agent":       agent.Name,
			"模型":            promptEvaluationModelForAgent(agent),
			"runtime":       runtime.Provider,
			"提示词版本":         prompt.Version,
			"实验维度评分":        dimensionScores,
			"trace/task id": uuidToString(taskID),
			"评估结论":          "等待智能体执行完成",
			"数据集版本数":        len(datasetVersionBindings),
		}),
		Evidence: mustJSONBytes(map[string]any{
			"task_id":         uuidToString(taskID),
			"chat_session_id": uuidToString(chatSessionID),
			"agent_id":        uuidToString(agent.ID),
			"runtime_id":      uuidToString(runtime.ID),
			"提示词版本":           prompt.Version,
			"数据集版本":           datasetVersionBindings,
			"实验维度评分":          dimensionScores,
		}),
		StartedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CreatedBy: createdBy,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create queued prompt evaluation run")
		return db.PromptEvaluationRun{}, false
	}
	if err := h.persistPromptEvaluationDimensionScores(r.Context(), run, dimensionScores, "run_metrics"); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist queued prompt evaluation dimension scores")
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
			FailureReason: "等待智能体执行完成",
			Evidence:      mustJSONBytes(map[string]any{"run_id": uuidToString(run.ID), "task_id": uuidToString(taskID)}),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create queued prompt evaluation trial")
			return db.PromptEvaluationRun{}, false
		}
	}
	return run, true
}

func (h *Handler) persistPromptEvaluationDimensionScores(ctx context.Context, run db.PromptEvaluationRun, scores []promptEvaluationExperimentDimensionScore, source string) error {
	if len(scores) == 0 {
		return nil
	}
	if err := h.Queries.DeletePromptEvaluationDimensionScoresByRun(ctx, db.DeletePromptEvaluationDimensionScoresByRunParams{
		WorkspaceID: run.WorkspaceID,
		RunID:       run.ID,
	}); err != nil {
		return err
	}
	for _, score := range scores {
		dimensionName := strings.TrimSpace(score.DimensionName)
		if dimensionName == "" {
			dimensionName = "维度 " + strconv.Itoa(int(score.DimensionIndex)+1)
		}
		if _, err := h.Queries.UpsertPromptEvaluationDimensionScore(ctx, db.UpsertPromptEvaluationDimensionScoreParams{
			WorkspaceID:    run.WorkspaceID,
			RunID:          run.ID,
			AssetID:        run.AssetID,
			PromptID:       run.PromptID,
			DimensionIndex: score.DimensionIndex,
			DimensionName:  dimensionName,
			Score:          score.Score,
			PassedCases:    int32(score.PassedCases),
			TotalCases:     int32(score.TotalCases),
			Status:         promptEvaluationDimensionScoreStatus(score.Status),
			Rule:           score.Rule,
			Evidence:       score.Evidence,
			Source:         source,
		}); err != nil {
			return err
		}
	}
	return nil
}

func promptEvaluationDimensionScoreStatus(status string) string {
	switch status {
	case "待执行", "已评分", "无用例":
		return status
	default:
		return "待执行"
	}
}

func (h *Handler) promptEvaluationCasesForAsset(w http.ResponseWriter, r *http.Request, asset db.PromptEvaluationAsset) ([]map[string]any, bool) {
	rows, err := h.Queries.ListPromptEvaluationCases(r.Context(), db.ListPromptEvaluationCasesParams{
		WorkspaceID: asset.WorkspaceID,
		AssetID:     asset.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation cases")
		return nil, false
	}
	executableRows := make([]db.PromptEvaluationCase, 0, len(rows))
	for _, row := range rows {
		if row.Status == "启用" || row.Status == "active" {
			executableRows = append(executableRows, row)
		}
	}
	if len(executableRows) == 0 {
		return promptEvaluationCases(decodePayloadObject(asset.Payload)), true
	}
	assertions, err := h.Queries.ListPromptEvaluationCaseAssertions(r.Context(), db.ListPromptEvaluationCaseAssertionsParams{
		WorkspaceID: asset.WorkspaceID,
		AssetID:     asset.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation case assertions")
		return nil, false
	}
	assertionsByCase := promptEvaluationAssertionsByCase(assertions)
	cases := make([]map[string]any, 0, len(executableRows))
	for _, row := range executableRows {
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

func promptEvaluationWeakDimensionSummaries(rows []db.ListPromptEvaluationDimensionScoreSummariesRow) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		weak := row.LatestStatus != "已评分" || row.TotalCases == 0 || row.Score < 1
		if !weak {
			continue
		}
		priority := "中"
		if row.LatestStatus != "已评分" || row.TotalCases == 0 || row.Score < 0.5 {
			priority = "高"
		}
		result = append(result, map[string]any{
			"维度名称":  row.DimensionName,
			"维度序号":  row.DimensionIndex,
			"得分":    row.Score,
			"通过用例数": row.PassedCases,
			"总用例数":  row.TotalCases,
			"运行次数":  row.RunCount,
			"已评分次数": row.ScoredRunCount,
			"最新状态":  row.LatestStatus,
			"最新证据":  row.LatestEvidence,
			"评分规则":  row.LatestRule,
			"优先级":   priority,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		priorityRank := map[string]int{"高": 0, "中": 1, "低": 2}
		leftPriority := priorityRank[stringFromAny(result[i]["优先级"])]
		rightPriority := priorityRank[stringFromAny(result[j]["优先级"])]
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		leftScore := floatFromAny(result[i]["得分"])
		rightScore := floatFromAny(result[j]["得分"])
		if leftScore != rightScore {
			return leftScore < rightScore
		}
		return stringFromAny(result[i]["维度名称"]) < stringFromAny(result[j]["维度名称"])
	})
	return result
}

func promptEvaluationCandidatePriority(weakDimensions []map[string]any) string {
	for _, item := range weakDimensions {
		if stringFromAny(item["优先级"]) == "高" {
			return "高"
		}
	}
	if len(weakDimensions) > 0 {
		return "中"
	}
	return "低"
}

func promptEvaluationDefaultWeakDimensionSummaries(run db.PromptEvaluationRun) []map[string]any {
	if !promptEvaluationRunHasFailure(run) {
		return nil
	}
	totalCases := int(run.TotalCases)
	if totalCases <= 0 {
		totalCases = 1
	}
	defaults := promptEvaluationDefaultExperimentDimensions()
	result := make([]map[string]any, 0, len(defaults))
	for idx, item := range defaults {
		result = append(result, map[string]any{
			"维度名称":  item.Name,
			"维度序号":  int32(idx),
			"得分":    0.0,
			"通过用例数": 0,
			"总用例数":  totalCases,
			"运行次数":  1,
			"已评分次数": 0,
			"最新状态":  "待执行",
			"最新证据":  "运行缺少实验维度评分事实，按默认实验维度标记为失败复盘重点",
			"评分规则":  promptEvaluationDimensionRule(item.Name),
			"优先级":   "高",
		})
	}
	return result
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
		rationale = "基于失败用例和真实智能体 task 证据补充中文输出约束、失败处理要求、证据字段和验收口径；原提示词不被自动替换，必须人工确认后发布。"
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
	if weakDimensions, ok := sourceSummary["失败维度"].([]map[string]any); ok && len(weakDimensions) > 0 {
		lines = append(lines, "", "维度优先级：")
		for _, item := range weakDimensions {
			name := stringFromAny(item["维度名称"])
			if name == "" {
				name = "未命名维度"
			}
			lines = append(lines, "- "+name+"："+stringFromAny(item["优先级"])+"优先级，得分 "+stringFromAny(item["得分"])+"，证据："+truncatePromptEvaluationEvidence(stringFromAny(item["最新证据"]), 160))
		}
		rationale = "基于失败用例、维度评分弱项和真实运行证据补充中文输出约束、失败处理要求、证据字段和验收口径；原提示词不被自动替换，必须人工确认后发布。"
	}
	if runtimeEvidence, ok := sourceSummary["真实Agent运行证据"].(map[string]any); ok {
		lines = append(lines, "", "真实智能体输出摘要：")
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
			"【智能体优化候选】",
			"来源失败运行：" + uuidToString(sourceRun.ID),
			"来源智能体优化运行：" + uuidToString(agentRun.ID),
			"失败原因：" + failureReason,
			"",
			"智能体优化输出摘要：",
			truncatePromptEvaluationEvidence(firstNonEmptyPromptEvaluationString(output.Raw, output.Rationale, "智能体未返回结构化候选正文，已基于运行证据生成待确认候选。"), 1200),
			"",
			"请在后续执行中严格遵守：",
			"1. 全部输出使用中文，明确需求边界、影响范围和验收条件。",
			"2. 输出必须包含可观测证据：耗时、执行智能体、模型、runtime、trace/task id、失败原因和评估结论。",
			"3. 对失败场景给出缺失字段、失败用例、修复建议和下一步人工确认点。",
			"4. 不要自动替换生产提示词；发布前必须由验收者确认。",
		}, "\n")
	}
	lines := []string{
		candidateContent,
		"",
		"【智能体优化运行证据】",
		"来源失败运行：" + uuidToString(sourceRun.ID),
		"来源智能体优化运行：" + uuidToString(agentRun.ID),
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
	rationale := "由真实智能体优化运行输出自动生成候选；原提示词不被自动替换，必须人工确认后发布。"
	if output.Rationale != "" {
		rationale = "由真实智能体优化运行输出自动生成候选：" + truncatePromptEvaluationEvidence(output.Rationale, 240)
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
		"任务类型":           "智能体优化运行",
		"来源运行":           uuidToString(run.ID),
		"来源资产":           uuidToString(run.AssetID),
		"来源提示词":          uuidToString(prompt.ID),
		"提示词名称":          prompt.Name,
		"原始提示词内容":        prompt.Content,
		"失败摘要":           sourceSummary,
		"cases":          cases,
		"优化目标": []string{
			"基于失败用例和真实智能体 task 证据生成候选提示词正文。",
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
	return prompt.Name + " 智能体优化运行 " + run.CreatedAt.Time.Format("20060102") + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
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
	agents, err := h.Queries.ListAllAgents(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agents for training evaluation")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	if agentRow, runtimeRow, ok := h.findSOPPromptEvaluationAgent(r.Context(), workspaceID, member, agents); ok {
		return agentRow, runtimeRow, true
	}
	instructions := promptEvaluationAgentInstructions()
	for _, existing := range agents {
		if existing.Name != promptEvaluationAgentName && existing.Name != legacyPromptEvaluationAgentName {
			continue
		}
		if uuidToString(existing.RuntimeID) == uuidToString(runtime.ID) &&
			existing.Model.String == promptEvaluationAgentModel() &&
			existing.Instructions == instructions &&
			existing.Name == promptEvaluationAgentName {
			if existing.ArchivedAt.Valid {
				restored, err := h.Queries.RestoreAgent(r.Context(), existing.ID)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "failed to restore training evaluation agent")
					return db.Agent{}, db.AgentRuntime{}, false
				}
				return restored, runtime, true
			}
			return existing, runtime, true
		}
		updated, err := h.Queries.UpdateAgent(r.Context(), db.UpdateAgentParams{
			ID:           existing.ID,
			Name:         pgtype.Text{String: promptEvaluationAgentName, Valid: true},
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
		if updated.ArchivedAt.Valid {
			updated, err = h.Queries.RestoreAgent(r.Context(), updated.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to restore training evaluation agent")
				return db.Agent{}, db.AgentRuntime{}, false
			}
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
		Scope:              "workspace",
		MaxConcurrentTasks: defaultAgentMaxConcurrentTasks,
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

func (h *Handler) findSOPPromptEvaluationAgent(ctx context.Context, workspaceID pgtype.UUID, member db.Member, agents []db.Agent) (db.Agent, db.AgentRuntime, bool) {
	required := map[string]bool{
		"PM":            false,
		"01-clarify":    false,
		"02-design":     false,
		"03-task-split": false,
		"04-implement":  false,
		"05-verify":     false,
	}
	aliases := map[string]string{
		"PM-项目经理": "PM",
		"pm":      "PM",
		"01-需求澄清": "01-clarify",
		"02-方案设计": "02-design",
		"03-任务拆分": "03-task-split",
		"04-开发":   "04-implement",
		"05-验证测试": "05-verify",
		"05-测试":   "05-verify",
	}
	var verifier *db.Agent
	for i := range agents {
		key := agents[i].Name
		if alias, ok := aliases[key]; ok {
			key = alias
		}
		if _, ok := required[key]; ok {
			required[key] = true
		}
		if key == "05-verify" {
			verifier = &agents[i]
		}
	}
	for _, present := range required {
		if !present {
			return db.Agent{}, db.AgentRuntime{}, false
		}
	}
	if verifier == nil {
		return db.Agent{}, db.AgentRuntime{}, false
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
		ID:          verifier.RuntimeID,
		WorkspaceID: workspaceID,
	})
	if err != nil || runtime.Status != "online" || !canUseRuntimeForAgent(member, runtime) {
		return db.Agent{}, db.AgentRuntime{}, false
	}
	if !runtime.LastSeenAt.Valid || time.Since(runtime.LastSeenAt.Time) > promptEvaluationRuntimeFreshTTL {
		return db.Agent{}, db.AgentRuntime{}, false
	}
	return *verifier, runtime, true
}

func (h *Handler) selectPromptEvaluationExecutionAgent(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, ownerID pgtype.UUID, member db.Member, payload map[string]any) (db.Agent, db.AgentRuntime, bool) {
	requestedAgentID := promptEvaluationRequestedAgentID(payload)
	if requestedAgentID == "" {
		return h.ensurePromptEvaluationAgent(w, r, workspaceID, ownerID, member)
	}
	agentID, err := util.ParseUUID(requestedAgentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "执行智能体标识无效")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "执行智能体不属于当前工作区")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "执行智能体已归档，不能创建真实调试任务")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
		ID:          agent.RuntimeID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "执行智能体绑定的运行时不可用")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	if !canUseRuntimeForAgent(member, runtime) {
		writeError(w, http.StatusForbidden, "当前成员不能使用该执行智能体绑定的运行时")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	if runtime.Status != "online" {
		writeError(w, http.StatusServiceUnavailable, "执行智能体绑定的运行时不在线，请先启动运行时")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	return agent, runtime, true
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

func promptEvaluationRequestedAgentID(payload map[string]any) string {
	for _, raw := range []any{
		payload["执行智能体"],
		firstValue(payload, "agent_id", "execution_agent_id", "target_agent_id", "执行智能体标识", "目标智能体标识"),
		firstValue(payload, "execution_agent", "target_agent"),
		firstValue(asMap(payload["历史调试载荷"]), "执行智能体", "execution_agent", "agent_id", "execution_agent_id", "target_agent_id", "执行智能体标识", "目标智能体标识"),
		firstValue(asMap(payload["运行环境"]), "执行智能体", "execution_agent", "agent_id", "execution_agent_id", "target_agent_id", "执行智能体标识", "目标智能体标识"),
	} {
		if id := promptEvaluationAgentIDFromAny(raw); id != "" {
			return id
		}
	}
	return ""
}

func promptEvaluationAgentIDFromAny(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		return stringFromAny(firstValue(v, "agent_id", "id", "智能体标识", "执行智能体标识"))
	default:
		return ""
	}
}

func promptEvaluationModelForAgent(agent db.Agent) string {
	if agent.Model.Valid && strings.TrimSpace(agent.Model.String) != "" {
		return strings.TrimSpace(agent.Model.String)
	}
	return promptEvaluationAgentModel()
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
			return promptEvaluationRuntimeReadinessResponse("无权限", providerName+" 无权限", "当前工作区存在 "+providerName+" 运行时，但你没有绑定或使用权限。", "请让运行时所有者将 "+providerName+" 运行时设为公开，或由工作区管理员为训练评估智能体绑定可用运行时。", nil, checkedAt), nil
		}
		return promptEvaluationRuntimeReadinessResponse("缺失", providerName+" 缺失", "当前工作区未发现 "+providerName+" 运行时，评测运行不能执行 "+promptEvaluationAgentModel()+"。", "安装并配置 "+provider+"，启动 multica 守护进程，等待 /api/runtimes 出现 provider="+provider+" 且 status=online 的运行时。", nil, checkedAt), nil
	}
	ageSeconds := promptEvaluationRuntimeAgeSeconds(*best, checkedAt)
	respRuntime := runtimeToResponse(*best)
	if best.Status != "online" {
		return promptEvaluationRuntimeReadinessResponse("离线", providerName+" 离线", "已注册 "+providerName+" runtime「"+best.Name+"」，但当前状态是离线，不能创建真实智能体任务。", "启动 multica daemon，并确认 "+provider+" 可执行文件在 PATH 中，或设置对应 MULTICA_<PROVIDER>_PATH 后重启 daemon。", &respRuntime, checkedAt), nil
	}
	if !best.LastSeenAt.Valid || checkedAt.Sub(best.LastSeenAt.Time) > promptEvaluationRuntimeFreshTTL {
		return promptEvaluationRuntimeReadinessResponse("过期", providerName+" 心跳过期", providerName+" runtime「"+best.Name+"」状态仍是 online，但最近心跳已经超过 2 分钟，不能证明当前可执行。", "检查 multica daemon 是否仍在运行，确认网络和心跳正常后等待 last_seen_at 刷新，再创建真实智能体任务。", &respRuntime, checkedAt), nil
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
		resp := promptEvaluationRuntimeReadinessResponse("容量受限", "模型额度受限", detail, "如果持续出现 429/529，请申请 "+fallbackPromptEvaluationAgentModel+" 模型额度或让管理员调整 Agent 模型配置。", &respRuntime, checkedAt)
		resp.LastSeenAgeSeconds = ageSeconds
		return resp, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PromptEvaluationRuntimeReadinessResponse{}, err
	}
	resp := promptEvaluationRuntimeReadinessResponse("就绪", providerName+" 在线", "已发现在线且心跳新鲜的 "+providerName+" 运行时「"+best.Name+"」，可以作为 "+promptEvaluationAgentModel()+" 的真实执行目标。", "无需修复；下一步应创建真实智能体任务并采集链路追踪、令牌、成本和输出。", &respRuntime, checkedAt)
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
		"不要修改业务代码，不要创建任务，不要泄露密钥。",
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
	dimensionScores := buildPromptEvaluationExperimentDimensionScores(promptEvaluationExperimentDimensionsForAsset(asset.AssetType, payload), results)
	return promptEvaluationRunResult{
		RunAt:             time.Now().UTC().Format(time.RFC3339),
		AssetType:         asset.AssetType,
		PromptName:        prompt.Name,
		PromptVersion:     prompt.Version,
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
		TraceTaskID:       "未创建智能体任务",
		FailureReason:     failureReason,
		Conclusion:        conclusion,
		MissingVarCount:   missingCount,
		CaseResults:       results,
		DimensionScores:   dimensionScores,
	}
}

func buildPromptEvaluationExperimentDimensionScores(dimensions []normalizedPromptEvaluationExperimentDimension, results []promptEvaluationCaseRunResult) []promptEvaluationExperimentDimensionScore {
	if len(dimensions) == 0 {
		return nil
	}
	scores := make([]promptEvaluationExperimentDimensionScore, 0, len(dimensions))
	for idx, dimension := range dimensions {
		name := strings.TrimSpace(dimension.Name)
		if name == "" {
			name = "维度 " + strconv.Itoa(idx+1)
		}
		passed := 0
		rule := promptEvaluationDimensionRule(name)
		for _, result := range results {
			if promptEvaluationDimensionCasePassed(name, result) {
				passed++
			}
		}
		total := len(results)
		score := 0.0
		status := "无用例"
		if total > 0 {
			score = float64(passed) / float64(total)
			status = "已评分"
		}
		scores = append(scores, promptEvaluationExperimentDimensionScore{
			DimensionIndex: int32(idx),
			DimensionName:  name,
			Score:          score,
			PassedCases:    passed,
			TotalCases:     total,
			Status:         status,
			Rule:           rule,
			Evidence:       fmt.Sprintf("%s：%d/%d", rule, passed, total),
		})
	}
	return scores
}

func pendingPromptEvaluationExperimentDimensionScores(dimensions []normalizedPromptEvaluationExperimentDimension, totalCases int) []promptEvaluationExperimentDimensionScore {
	if len(dimensions) == 0 {
		return nil
	}
	scores := make([]promptEvaluationExperimentDimensionScore, 0, len(dimensions))
	for idx, dimension := range dimensions {
		name := strings.TrimSpace(dimension.Name)
		if name == "" {
			name = "维度 " + strconv.Itoa(idx+1)
		}
		rule := promptEvaluationDimensionRule(name)
		scores = append(scores, promptEvaluationExperimentDimensionScore{
			DimensionIndex: int32(idx),
			DimensionName:  name,
			Score:          0,
			PassedCases:    0,
			TotalCases:     totalCases,
			Status:         "待执行",
			Rule:           rule,
			Evidence:       "等待智能体返回结构化评估结果后再评分",
		})
	}
	return scores
}

func promptEvaluationDimensionCasePassed(dimensionName string, result promptEvaluationCaseRunResult) bool {
	normalized := strings.ToLower(strings.TrimSpace(dimensionName))
	switch {
	case strings.Contains(normalized, "缺失变量") || strings.Contains(normalized, "变量"):
		return len(result.MissingVariables) == 0
	case strings.Contains(normalized, "中文"):
		return containsHanRune(result.RenderedPrompt)
	case strings.Contains(normalized, "命中") || strings.Contains(normalized, "覆盖") || strings.Contains(normalized, "期望"):
		return len(result.ExpectedContains) == len(result.MatchedContains)
	default:
		return result.Status == "通过"
	}
}

func promptEvaluationDimensionRule(dimensionName string) string {
	normalized := strings.ToLower(strings.TrimSpace(dimensionName))
	switch {
	case strings.Contains(normalized, "缺失变量") || strings.Contains(normalized, "变量"):
		return "逐用例检查缺失变量数为 0"
	case strings.Contains(normalized, "中文"):
		return "逐用例检查渲染提示词包含中文字符"
	case strings.Contains(normalized, "命中") || strings.Contains(normalized, "覆盖") || strings.Contains(normalized, "期望"):
		return "逐用例检查期望内容全部命中"
	default:
		return "逐用例沿用总体通过状态"
	}
}

func containsHanRune(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func promptEvaluationCases(payload map[string]any) []map[string]any {
	raw := payload["cases"]
	if arr, ok := raw.([]map[string]any); ok && len(arr) > 0 {
		cases := make([]map[string]any, len(arr))
		copy(cases, arr)
		return cases
	}
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
		"写入策略":           "新建和更新统一写入规范 cases。",
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

func applyPromptEvaluationOptimizationRunContract(payload map[string]any, assetID string, sourceRunID string, runIndex map[string]any, eventName string) {
	if payload == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rounds := promptEvaluationAnyList(payload["优化轮次"])
	roundIndex := intFromAny(runIndex["轮次"])
	if roundIndex <= 0 {
		roundIndex = len(rounds) + 1
		runIndex["轮次"] = roundIndex
	}
	retryIndex := intFromAny(runIndex["重试序号"])
	round := map[string]any{
		"轮次":              roundIndex,
		"重试序号":            retryIndex,
		"运行ID":            stringFromAny(runIndex["run_id"]),
		"来源运行":            sourceRunID,
		"状态":              stringFromAny(runIndex["状态"]),
		"执行Agent":         stringFromAny(runIndex["执行Agent"]),
		"模型":              stringFromAny(runIndex["模型"]),
		"runtime":         stringFromAny(runIndex["runtime"]),
		"runtime_id":      stringFromAny(runIndex["runtime_id"]),
		"trace/task id":   stringFromAny(runIndex["trace/task id"]),
		"chat_session_id": stringFromAny(runIndex["chat_session_id"]),
		"创建时间":            now,
		"验收口径": []string{
			"必须保留中文语义",
			"必须给出优化候选正文和逐条修改依据",
			"必须能回读 task、trace、消息、用量和人工确认状态",
		},
	}
	rounds = append([]any{round}, rounds...)
	if len(rounds) > 20 {
		rounds = rounds[:20]
	}
	logs := promptEvaluationAnyList(payload["日志流"])
	logs = append(logs, map[string]any{
		"seq":   len(logs) + 1,
		"事件":    eventName,
		"状态":    stringFromAny(runIndex["状态"]),
		"轮次":    roundIndex,
		"运行ID":  stringFromAny(runIndex["run_id"]),
		"任务ID":  stringFromAny(runIndex["trace/task id"]),
		"消息":    eventName + "已入队，等待智能体输出候选并回写证据",
		"记录时间":  now,
		"可回读证据": []string{"运行历史", "运行证据", "task messages", "trace 事件", "task usage"},
	})
	if len(logs) > 50 {
		logs = logs[len(logs)-50:]
	}
	payload["schema"] = "multica.training_evaluation.optimization_run.v2"
	payload["语义版本"] = "multica.training_evaluation.optimization_run.v2"
	payload["优化运行契约"] = map[string]any{
		"schema": "multica.training_evaluation.optimization_run.v2",
		"资产ID":   assetID,
		"来源运行":   sourceRunID,
		"轮次字段":   "优化轮次",
		"日志字段":   "日志流",
		"重试入口":   "/api/prompt-evaluation-assets/" + assetID + "/agent-run",
		"候选发布入口": "/api/prompt-evaluation-optimization-candidates/{candidate_id}/publish",
		"人工确认要求": "优化运行只产生候选和证据；发布新版本必须经过人工确认。",
		"数据回读要求": []string{"资产详情", "运行历史", "运行证据", "优化候选", "提示词版本历史"},
	}
	payload["优化轮次"] = rounds
	payload["日志流"] = logs
	currentRetryCount := retryIndex
	if currentRetryCount < promptEvaluationOptimizationRetryCount(payload) {
		currentRetryCount = promptEvaluationOptimizationRetryCount(payload)
	}
	payload["重试策略"] = map[string]any{
		"允许重试":   true,
		"当前重试次数": currentRetryCount,
		"重试入口":   "/api/prompt-evaluation-assets/" + assetID + "/agent-run",
		"重试说明":   "重试会创建新的真实智能体任务和运行记录，不覆盖已有轮次。",
	}
}

func promptEvaluationOptimizationRoundCount(payload map[string]any) int {
	return len(promptEvaluationAnyList(payload["优化轮次"]))
}

func promptEvaluationOptimizationRetryCount(payload map[string]any) int {
	count := 0
	for _, item := range promptEvaluationAnyList(payload["优化轮次"]) {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		retry := intFromAny(record["重试序号"])
		if retry > count {
			count = retry
		}
	}
	return count
}

func promptEvaluationAnyList(raw any) []any {
	if values, ok := raw.([]any); ok {
		return values
	}
	return nil
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

func floatFromAny(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	case string:
		parsed, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed
	default:
		return 0
	}
}

func promptEvaluationAgentRunMessage(assetType string) string {
	if assetType == promptEvaluationAssetOptimize {
		return "优化运行重试已入队；请通过运行历史、日志流和证据面板追踪新轮次。"
	}
	return "真实智能体任务已入队；请通过 task messages、usage 和运行历史追踪结果。"
}

func promptEvaluationDatasetRowsFingerprint(rows []db.PromptEvaluationDatasetRow) string {
	snapshot := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		snapshot = append(snapshot, map[string]any{
			"row_index":         row.RowIndex,
			"row_name":          row.RowName,
			"variables":         decodeJSONDefault(row.Variables, map[string]any{}),
			"expected_contains": decodeJSONDefault(row.ExpectedContains, []any{}),
			"expected":          decodeJSONDefault(row.Expected, map[string]any{}),
			"tags":              decodeJSONDefault(row.Tags, []any{}),
			"source":            row.Source,
		})
	}
	sum := sha256.Sum256(mustJSONBytes(snapshot))
	return fmt.Sprintf("%x", sum[:])
}

func promptEvaluationDatasetVersionRowFingerprint(row db.PromptEvaluationDatasetVersionRow) string {
	snapshot := map[string]any{
		"row_index":         row.RowIndex,
		"row_name":          row.RowName,
		"variables":         decodeJSONDefault(row.Variables, map[string]any{}),
		"expected_contains": decodeJSONDefault(row.ExpectedContains, []any{}),
		"expected":          decodeJSONDefault(row.Expected, map[string]any{}),
		"tags":              decodeJSONDefault(row.Tags, []any{}),
		"source":            row.Source,
	}
	sum := sha256.Sum256(mustJSONBytes(snapshot))
	return fmt.Sprintf("%x", sum[:])
}

func buildPromptEvaluationDatasetVersionDiff(baseRows []db.PromptEvaluationDatasetVersionRow, targetRows []db.PromptEvaluationDatasetVersionRow) PromptEvaluationDatasetVersionDiffResponse {
	baseByIndex := make(map[int32]db.PromptEvaluationDatasetVersionRow, len(baseRows))
	targetByIndex := make(map[int32]db.PromptEvaluationDatasetVersionRow, len(targetRows))
	indexSet := map[int32]bool{}
	for _, row := range baseRows {
		baseByIndex[row.RowIndex] = row
		indexSet[row.RowIndex] = true
	}
	for _, row := range targetRows {
		targetByIndex[row.RowIndex] = row
		indexSet[row.RowIndex] = true
	}
	indexes := make([]int, 0, len(indexSet))
	for index := range indexSet {
		indexes = append(indexes, int(index))
	}
	sort.Ints(indexes)

	resp := PromptEvaluationDatasetVersionDiffResponse{
		Summary: map[string]int{
			"新增":  0,
			"删除":  0,
			"变更":  0,
			"未变更": 0,
		},
		Added:     []PromptEvaluationDatasetVersionRowResponse{},
		Removed:   []PromptEvaluationDatasetVersionRowResponse{},
		Changed:   []PromptEvaluationDatasetVersionChangedRow{},
		Unchanged: []PromptEvaluationDatasetVersionRowResponse{},
	}
	for _, rawIndex := range indexes {
		index := int32(rawIndex)
		base, hasBase := baseByIndex[index]
		target, hasTarget := targetByIndex[index]
		switch {
		case !hasBase && hasTarget:
			resp.Added = append(resp.Added, promptEvaluationDatasetVersionRowToResponse(target))
			resp.Summary["新增"]++
		case hasBase && !hasTarget:
			resp.Removed = append(resp.Removed, promptEvaluationDatasetVersionRowToResponse(base))
			resp.Summary["删除"]++
		case promptEvaluationDatasetVersionRowFingerprint(base) != promptEvaluationDatasetVersionRowFingerprint(target):
			resp.Changed = append(resp.Changed, PromptEvaluationDatasetVersionChangedRow{
				RowIndex: index,
				Base:     promptEvaluationDatasetVersionRowToResponse(base),
				Target:   promptEvaluationDatasetVersionRowToResponse(target),
			})
			resp.Summary["变更"]++
		default:
			resp.Unchanged = append(resp.Unchanged, promptEvaluationDatasetVersionRowToResponse(target))
			resp.Summary["未变更"]++
		}
	}
	return resp
}

func promptEvaluationPayloadCasesFromDatasetVersionRows(rows []db.PromptEvaluationDatasetVersionRow) []map[string]any {
	cases := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		cases = append(cases, map[string]any{
			"name":              row.RowName,
			"case_name":         row.RowName,
			"名称":                row.RowName,
			"variables":         decodeJSONDefault(row.Variables, map[string]any{}),
			"变量":                decodeJSONDefault(row.Variables, map[string]any{}),
			"expected_contains": decodeJSONDefault(row.ExpectedContains, []any{}),
			"期望包含":              decodeJSONDefault(row.ExpectedContains, []any{}),
			"expected":          decodeJSONDefault(row.Expected, map[string]any{}),
			"期望":                decodeJSONDefault(row.Expected, map[string]any{}),
			"tags":              decodeJSONDefault(row.Tags, []any{}),
			"标签":                decodeJSONDefault(row.Tags, []any{}),
		})
	}
	return cases
}

func promptEvaluationDatasetVersionRestoreMetadata(version db.PromptEvaluationDatasetVersion, requestMetadata []byte) []byte {
	metadata := map[string]any{
		"来源":        "数据集版本恢复",
		"恢复来源版本":    version.Version,
		"恢复来源版本标识":  uuidToString(version.ID),
		"恢复来源版本名称":  version.VersionLabel,
		"恢复来源版本行指纹": version.RowFingerprint,
		"恢复时间":      time.Now().Format(time.RFC3339),
	}
	if len(requestMetadata) > 0 {
		var extra map[string]any
		if err := json.Unmarshal(requestMetadata, &extra); err == nil {
			for key, value := range extra {
				metadata[key] = value
			}
		}
	}
	return mustJSONBytes(metadata)
}

func promptEvaluationDatasetVersionSummary(version db.PromptEvaluationDatasetVersion) map[string]any {
	return map[string]any{
		"dataset_version_id": uuidToString(version.ID),
		"version":            version.Version,
		"version_label":      version.VersionLabel,
		"row_count":          version.RowCount,
		"row_fingerprint":    version.RowFingerprint,
		"created_at":         timestampToString(version.CreatedAt),
	}
}

func (h *Handler) promptEvaluationDatasetVersionBindings(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, payload map[string]any) ([]map[string]any, bool) {
	datasetIDs := promptEvaluationLinkedDatasetIDs(payload)
	explicit := promptEvaluationExplicitDatasetVersionRefs(payload)
	if len(datasetIDs) == 0 && len(explicit) == 0 {
		return nil, true
	}
	bindings := make([]map[string]any, 0, len(datasetIDs)+len(explicit))
	seenVersions := map[string]bool{}
	explicitDatasets := map[string]bool{}
	for _, ref := range explicit {
		datasetID, err := util.ParseUUID(ref.DatasetAssetID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "linked dataset version has invalid dataset_asset_id")
			return nil, false
		}
		versionID, err := util.ParseUUID(ref.DatasetVersionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "linked dataset version has invalid dataset_version_id")
			return nil, false
		}
		datasetKey := uuidToString(datasetID)
		versionKey := uuidToString(versionID)
		explicitDatasets[datasetKey] = true
		if seenVersions[datasetKey+"."+versionKey] {
			continue
		}
		version, err := h.Queries.GetPromptEvaluationDatasetVersionInAsset(r.Context(), db.GetPromptEvaluationDatasetVersionInAssetParams{
			WorkspaceID:    workspaceID,
			DatasetAssetID: datasetID,
			ID:             versionID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "linked dataset version does not belong to this workspace dataset")
				return nil, false
			}
			writeError(w, http.StatusInternalServerError, "failed to load linked dataset version")
			return nil, false
		}
		summary := promptEvaluationDatasetVersionSummary(version)
		summary["dataset_asset_id"] = datasetKey
		summary["绑定方式"] = "资产声明的明确数据集版本"
		if strings.TrimSpace(ref.DatasetName) != "" {
			summary["dataset_name"] = strings.TrimSpace(ref.DatasetName)
		}
		bindings = append(bindings, summary)
		seenVersions[datasetKey+"."+versionKey] = true
	}
	seen := map[string]bool{}
	for _, rawID := range datasetIDs {
		datasetID, err := util.ParseUUID(rawID)
		if err != nil {
			continue
		}
		key := uuidToString(datasetID)
		if key == "" || seen[key] || explicitDatasets[key] {
			continue
		}
		seen[key] = true
		version, err := h.Queries.GetLatestPromptEvaluationDatasetVersion(r.Context(), db.GetLatestPromptEvaluationDatasetVersionParams{
			WorkspaceID:    workspaceID,
			DatasetAssetID: datasetID,
		})
		if err != nil {
			continue
		}
		summary := promptEvaluationDatasetVersionSummary(version)
		summary["dataset_asset_id"] = key
		summary["绑定方式"] = "运行开始时读取最新数据集版本"
		bindings = append(bindings, summary)
	}
	return bindings, true
}

type promptEvaluationDatasetVersionRef struct {
	DatasetAssetID   string
	DatasetVersionID string
	DatasetName      string
}

func promptEvaluationExplicitDatasetVersionRefs(payload map[string]any) []promptEvaluationDatasetVersionRef {
	raw := firstValue(payload, "linked_dataset_versions", "数据集版本", "关联数据集版本")
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]promptEvaluationDatasetVersionRef, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		versionID := strings.TrimSpace(stringFromAny(firstValue(m, "dataset_version_id", "version_id", "数据集版本ID")))
		if versionID == "" {
			continue
		}
		datasetID := strings.TrimSpace(stringFromAny(firstValue(m, "dataset_id", "dataset_asset_id", "数据集ID")))
		if datasetID == "" {
			result = append(result, promptEvaluationDatasetVersionRef{DatasetVersionID: versionID})
			continue
		}
		result = append(result, promptEvaluationDatasetVersionRef{
			DatasetAssetID:   datasetID,
			DatasetVersionID: versionID,
			DatasetName:      strings.TrimSpace(stringFromAny(firstValue(m, "dataset_name", "数据集名称", "name", "名称"))),
		})
	}
	return result
}

func promptEvaluationLinkedDatasetIDs(payload map[string]any) []string {
	values := []any{
		firstValue(payload, "linked_dataset_ids", "dataset_ids", "数据集ID", "关联数据集ID"),
	}
	if nested, ok := firstValue(payload, "linked_dataset_versions", "数据集版本", "关联数据集版本").([]any); ok {
		for _, item := range nested {
			if m, ok := item.(map[string]any); ok {
				values = append(values, firstValue(m, "dataset_id", "dataset_asset_id", "数据集ID"))
			}
		}
	}
	result := []string{}
	for _, value := range values {
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				result = append(result, strings.TrimSpace(v))
			}
		case []any:
			for _, item := range v {
				if s := strings.TrimSpace(stringFromAny(item)); s != "" {
					result = append(result, s)
				}
			}
		case []string:
			for _, item := range v {
				if strings.TrimSpace(item) != "" {
					result = append(result, strings.TrimSpace(item))
				}
			}
		}
	}
	return result
}

func firstValue(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func asMap(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return map[string]any{}
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
