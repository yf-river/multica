package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
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
	defer func() { _ = tx.Rollback(r.Context()) }()
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
	defer func() { _ = tx.Rollback(r.Context()) }()
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
