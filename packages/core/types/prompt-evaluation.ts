import type { PromptLibraryItem } from "./prompt-library";
import type { TaskMessagePayload } from "./events";
import type { AgentRuntime, TaskTraceEvent } from "./agent";

export type PromptEvaluationAssetType = "数据集" | "测试套件" | "实验" | "优化运行";
export type PromptEvaluationAssetStatus = "启用" | "归档";
export type PromptEvaluationOptimizationCandidateStatus = "待确认" | "已发布" | "已拒绝";

export interface PromptEvaluationCase {
  case_name: string;
  variables: Record<string, unknown>;
  expected_contains: string[];
  tags?: string[];
}

export interface PromptEvaluationMetricSummary {
  总用例数: number;
  通过数: number;
  失败数: number;
  通过率: number;
  总耗时毫秒: number;
  平均耗时毫秒: number;
  输入token: number;
  输出token: number;
  预估成本: number;
  执行Agent: string;
  模型: string;
  runtime: string;
  "trace/task id": string;
  失败原因: string;
  评估结论: string;
}

export interface PromptEvaluationStructuredPayload extends Record<string, unknown> {
  schema_version: 1;
  schema: "multica.training_evaluation.payload.v1";
  语义版本: "multica.training_evaluation.v1";
  cases: PromptEvaluationCase[];
  payload_contract?: Record<string, unknown>;
  metric_contract?: string[];
  指标口径?: string[];
  最近运行?: PromptEvaluationMetricSummary;
  运行记录?: PromptEvaluationMetricSummary[];
}

export interface PromptEvaluationAsset {
  id: string;
  workspace_id: string;
  prompt_id: string | null;
  name: string;
  description: string;
  asset_type: PromptEvaluationAssetType;
  payload: Record<string, unknown> | PromptEvaluationStructuredPayload;
  status: PromptEvaluationAssetStatus;
  created_by: string | null;
  created_at: string;
  updated_at: string;
  structure_schema: string;
  structured_case_count: number;
  structured_variable_count: number;
  structured_assertion_count: number;
  linked_dataset_count: number;
  linked_prompt_count: number;
  evaluation_dimension_count: number;
  dataset_row_count: number;
  test_suite_case_count: number;
  experiment_dimension_count: number;
}

export interface PromptEvaluationDatasetVersion {
  id: string;
  workspace_id: string;
  dataset_asset_id: string;
  version: number;
  version_label: string;
  row_count: number;
  row_fingerprint: string;
  metadata: Record<string, unknown>;
  created_by: string | null;
  created_at: string;
}

export interface PromptEvaluationDatasetVersionRow {
  id: string;
  workspace_id: string;
  dataset_version_id: string;
  dataset_asset_id: string;
  source_row_id: string | null;
  case_id: string | null;
  row_index: number;
  row_name: string;
  variables: Record<string, unknown>;
  expected_contains: unknown[];
  expected: Record<string, unknown>;
  tags: unknown[];
  source: string;
  created_at: string;
}

export interface PromptEvaluationDatasetVersionChangedRow {
  row_index: number;
  base: PromptEvaluationDatasetVersionRow;
  target: PromptEvaluationDatasetVersionRow;
}

export interface PromptEvaluationDatasetVersionDiff {
  base_version: PromptEvaluationDatasetVersion;
  target_version: PromptEvaluationDatasetVersion;
  summary: Record<string, number>;
  added: PromptEvaluationDatasetVersionRow[];
  removed: PromptEvaluationDatasetVersionRow[];
  changed: PromptEvaluationDatasetVersionChangedRow[];
  unchanged: PromptEvaluationDatasetVersionRow[];
}

export interface RestorePromptEvaluationDatasetVersionRequest {
  version_label?: string;
  metadata?: Record<string, unknown>;
}

export interface RestorePromptEvaluationDatasetVersionResponse {
  asset: PromptEvaluationAsset;
  restored_from: PromptEvaluationDatasetVersion;
  restored_version: PromptEvaluationDatasetVersion;
  restored_cases: PromptEvaluationStructuredCase[];
}

export interface PromptEvaluationRun {
  id: string;
  workspace_id: string;
  asset_id: string;
  prompt_id: string | null;
  run_kind: "模板渲染检查" | "Agent执行";
  status: "已入队" | "运行中" | "通过" | "未通过" | "失败" | "已取消" | "需人工复核";
  trigger_source: string;
  agent_id: string | null;
  runtime_id: string | null;
  task_id: string | null;
  chat_session_id: string | null;
  model: string;
  runtime_provider: string;
  total_cases: number;
  passed_cases: number;
  failed_cases: number;
  pass_rate: number;
  total_duration_ms: number;
  average_duration_ms: number;
  input_tokens: number;
  output_tokens: number;
  estimated_cost: number;
  failure_reason: string;
  conclusion: string;
  metrics: Record<string, unknown>;
  evidence: Record<string, unknown>;
  started_at: string;
  completed_at: string;
  created_by: string | null;
  created_at: string;
  updated_at: string;
  review_decision: "" | "通过" | "未通过";
  review_note: string;
  reviewed_by: string | null;
  reviewed_at: string;
}

export interface PromptEvaluationTrial {
  id: string;
  run_id: string;
  workspace_id: string;
  asset_id: string;
  case_index: number;
  case_name: string;
  status: "待执行" | "通过" | "未通过" | "失败" | "已跳过" | "需人工复核";
  input: Record<string, unknown>;
  expected: Record<string, unknown>;
  output: unknown;
  rendered_prompt: string;
  input_tokens: number;
  output_tokens: number;
  duration_ms: number;
  failure_reason: string;
  evidence: Record<string, unknown>;
  created_at: string;
}

export interface PromptEvaluationTaskUsage {
  id: string;
  task_id: string;
  provider: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  estimated_cost?: number;
  priced?: boolean;
  created_at: string;
  updated_at: string;
}

export interface PromptEvaluationExecutionSpan {
  id: string;
  parent_id?: string;
  span_kind: string;
  span_name: string;
  status: string;
  seq: number;
  task_id?: string;
  tool?: string;
  provider?: string;
  model?: string;
  token_total: number;
  duration_ms: number;
  summary: string;
  details?: Record<string, unknown>;
  created_at?: string;
}

export interface PromptEvaluationToolCallChain {
  id: string;
  task_id?: string;
  tool?: string;
  status: "已配对" | "缺少结果" | "孤立结果" | string;
  use_seq?: number;
  result_seq?: number;
  use_span_id?: string;
  result_span_id?: string;
  input?: Record<string, unknown>;
  output?: string;
  duration_ms?: number;
  result_category?: string;
  failure_signal: boolean;
  failure_reason?: string;
  summary: string;
  created_at?: string;
  completed_at?: string;
}

export interface PromptEvaluationToolCallSummary {
  tool: string;
  total_calls: number;
  paired_calls: number;
  missing_result_calls: number;
  orphan_result_calls: number;
  average_duration_ms?: number;
  max_duration_ms?: number;
  slowest_tool_call_chain_id?: string;
  result_categories?: Record<string, number>;
  failure_signal_calls: number;
  needs_attention: boolean;
  summary: string;
}

export interface PromptEvaluationRunEvidence {
  run: PromptEvaluationRun;
  trials: PromptEvaluationTrial[];
  task_usage: PromptEvaluationTaskUsage[];
  task_messages: TaskMessagePayload[];
  trace_events: TaskTraceEvent[];
  execution_spans: PromptEvaluationExecutionSpan[];
  tool_call_chains: PromptEvaluationToolCallChain[];
  tool_call_summary: PromptEvaluationToolCallSummary[];
  execution_summary: Record<string, unknown>;
  evidence: Record<string, unknown>;
  上下文: Record<string, unknown>;
}

export type PromptEvaluationEvidenceSnapshotType = "手动归档" | "验收归档" | "自动归档";

export interface PromptEvaluationEvidenceSnapshot {
  id: string;
  workspace_id: string;
  run_id: string;
  snapshot_type: PromptEvaluationEvidenceSnapshotType;
  schema_version: string;
  summary: Record<string, unknown>;
  evidence?: Record<string, unknown>;
  created_by: string | null;
  created_at: string;
}

export interface ListPromptEvaluationEvidenceSnapshotsResponse {
  items: PromptEvaluationEvidenceSnapshot[];
  total: number;
}

export interface PromptEvaluationAssetEvidenceSnapshotSkip {
  run_id: string;
  reason: string;
}

export interface PromptEvaluationAssetEvidenceSnapshotResponse {
  asset_id: string;
  snapshot_type: PromptEvaluationEvidenceSnapshotType;
  total_runs: number;
  created_count: number;
  skipped_count: number;
  items: PromptEvaluationEvidenceSnapshot[];
  skipped: PromptEvaluationAssetEvidenceSnapshotSkip[];
}

export interface PromptEvaluationAssetEvidenceArchiveItem {
  run: PromptEvaluationRun;
  snapshots: PromptEvaluationEvidenceSnapshot[];
}

export interface PromptEvaluationAssetEvidenceArchivePackage {
  schema_version: string;
  generated_at: string;
  asset_id: string;
  snapshot_type: PromptEvaluationEvidenceSnapshotType;
  total_runs: number;
  archived_run_count: number;
  missing_run_count: number;
  asset: PromptEvaluationAsset;
  items: PromptEvaluationAssetEvidenceArchiveItem[];
  中文摘要: Record<string, unknown>;
}

export interface PromptEvaluationSummary {
  workspace_id: string;
  generated_at: string;
  last_run_at: string;
  指标: Record<string, number>;
  资产统计: Record<string, number>;
  运行状态: Record<string, number>;
  优化候选: Record<string, number>;
}

export type PromptEvaluationRuntimeReadinessStatus = "就绪" | "离线" | "过期" | "缺失" | "无权限" | "容量受限";

export interface PromptEvaluationRuntimeReadiness {
  status: PromptEvaluationRuntimeReadinessStatus;
  label: string;
  detail: string;
  fix: string;
  model: string;
  runtime: AgentRuntime | null;
  last_seen_age_seconds: number;
  checked_at: string;
}

export interface PromptEvaluationStructuredCase {
  id: string;
  workspace_id: string;
  asset_id: string;
  prompt_id: string | null;
  case_index: number;
  case_name: string;
  variables: Record<string, unknown>;
  expected_contains: unknown[];
  assertions: PromptEvaluationCaseAssertion[];
  input: Record<string, unknown>;
  expected: Record<string, unknown>;
  tags: unknown[];
  status: PromptEvaluationAssetStatus;
  source: string;
  created_by: string | null;
  created_at: string;
  updated_at: string;
}

export interface PromptEvaluationCaseAssertion {
  id: string;
  workspace_id: string;
  asset_id: string;
  case_id: string;
  assertion_index: number;
  assertion_type: "包含文本";
  expected_text: string;
  status: PromptEvaluationAssetStatus;
  source: string;
  created_at: string;
}

export interface PromptEvaluationExperimentDimension {
  id: string;
  workspace_id: string;
  experiment_asset_id: string;
  dimension_index: number;
  dimension_name: string;
  experiment_target: string;
  baseline_output: string;
  comparison_payload: Record<string, unknown>;
  status: PromptEvaluationAssetStatus;
  source: string;
  created_by: string | null;
  created_at: string;
  updated_at: string;
}

export type PromptEvaluationDimensionScoreStatus = "待执行" | "已评分" | "无用例";

export interface PromptEvaluationDimensionScore {
  id: string;
  workspace_id: string;
  run_id: string;
  asset_id: string;
  prompt_id: string | null;
  dimension_index: number;
  dimension_name: string;
  score: number;
  passed_cases: number;
  total_cases: number;
  status: PromptEvaluationDimensionScoreStatus;
  rule: string;
  evidence: string;
  source: string;
  created_at: string;
  updated_at: string;
}

export interface PromptEvaluationDimensionScoreSummary {
  workspace_id: string;
  asset_id: string;
  prompt_id: string | null;
  dimension_index: number;
  dimension_name: string;
  run_count: number;
  scored_run_count: number;
  passed_cases: number;
  total_cases: number;
  score: number;
  latest_status: PromptEvaluationDimensionScoreStatus;
  latest_rule: string;
  latest_evidence: string;
  latest_source: string;
  latest_scored_at: string;
}

export interface PromptEvaluationDimensionScoreTrend {
  workspace_id: string;
  asset_id: string;
  prompt_id: string | null;
  dimension_index: number;
  dimension_name: string;
  period: string;
  prompt_version: number;
  run_count: number;
  scored_run_count: number;
  passed_cases: number;
  total_cases: number;
  score: number;
  latest_status: PromptEvaluationDimensionScoreStatus;
  latest_rule: string;
  latest_evidence: string;
  latest_source: string;
  latest_scored_at: string;
}

export interface PromptEvaluationAgentRunResponse {
  asset: PromptEvaluationAsset;
  run: PromptEvaluationRun;
  task_id: string;
  chat_session_id: string;
  agent_id: string;
  runtime_id: string;
  model: string;
  status: string;
  message: string;
}

export interface CreatePromptEvaluationDatasetFromTracesRequest {
  task_ids?: string[];
  event_type?: string;
  limit?: number;
  expected_contains?: string[];
  tags?: string[];
}

export interface PromptEvaluationDatasetFromTracesResponse {
  asset: PromptEvaluationAsset;
  cases: PromptEvaluationStructuredCase[];
  trace_events: TaskTraceEvent[];
  created_count: number;
  skipped_count: number;
  source: "trace";
}

export interface PromptEvaluationOptimizationCandidate {
  id: string;
  workspace_id: string;
  asset_id: string;
  run_id: string;
  prompt_id: string;
  candidate_name: string;
  candidate_content: string;
  rationale: string;
  failed_case_count: number;
  source_failure_summary: Record<string, unknown>;
  source_prompt_snapshot: Record<string, unknown>;
  metrics: Record<string, unknown>;
  status: PromptEvaluationOptimizationCandidateStatus;
  published_prompt_id: string | null;
  published_at: string;
  created_by: string | null;
  created_at: string;
  updated_at: string;
}

export interface UpdatePromptEvaluationOptimizationCandidateRequest {
  candidate_name: string;
  candidate_content: string;
  rationale?: string;
  edit_note?: string;
}

export interface PublishPromptEvaluationOptimizationCandidateResponse {
  candidate: PromptEvaluationOptimizationCandidate;
  prompt: PromptLibraryItem;
}

export interface ListPromptEvaluationAssetsResponse {
  items: PromptEvaluationAsset[];
  total: number;
}

export interface ListPromptEvaluationDatasetVersionsResponse {
  items: PromptEvaluationDatasetVersion[];
  total: number;
}

export interface ListPromptEvaluationDatasetVersionRowsResponse {
  items: PromptEvaluationDatasetVersionRow[];
  total: number;
}

export interface ListPromptEvaluationRunsResponse {
  items: PromptEvaluationRun[];
  total: number;
}

export interface ListPromptEvaluationTrialsResponse {
  items: PromptEvaluationTrial[];
  total: number;
}

export interface ListPromptEvaluationCasesResponse {
  items: PromptEvaluationStructuredCase[];
  total: number;
}

export interface ListPromptEvaluationExperimentDimensionsResponse {
  items: PromptEvaluationExperimentDimension[];
  total: number;
}

export interface ListPromptEvaluationDimensionScoresResponse {
  items: PromptEvaluationDimensionScore[];
  total: number;
}

export interface ListPromptEvaluationDimensionScoreSummariesResponse {
  items: PromptEvaluationDimensionScoreSummary[];
  total: number;
}

export interface ListPromptEvaluationDimensionScoreTrendsResponse {
  items: PromptEvaluationDimensionScoreTrend[];
  total: number;
}

export interface ListPromptEvaluationOptimizationCandidatesResponse {
  items: PromptEvaluationOptimizationCandidate[];
  total: number;
}

export interface ListPromptEvaluationAssetsParams {
  prompt_id?: string;
  asset_type?: PromptEvaluationAssetType;
  status?: PromptEvaluationAssetStatus;
}

export interface ListPromptEvaluationRunsParams {
  asset_id?: string;
  status?: PromptEvaluationRun["status"];
  since?: string | null;
  limit?: number;
}

export interface ReviewPromptEvaluationRunRequest {
  decision: "通过" | "未通过";
  note?: string;
}

export interface ListPromptEvaluationCasesParams {
  asset_id?: string;
  status?: PromptEvaluationAssetStatus;
  source?: "manual" | "trace" | "payload";
  tag?: string;
  keyword?: string;
  limit?: number;
}

export interface ListPromptEvaluationExperimentDimensionsParams {
  asset_id?: string;
  status?: PromptEvaluationAssetStatus;
}

export interface ListPromptEvaluationDimensionScoresParams {
  run_id?: string;
  asset_id?: string;
  prompt_id?: string;
  status?: PromptEvaluationDimensionScoreStatus;
}

export interface ListPromptEvaluationDimensionScoreSummariesParams {
  asset_id?: string;
  prompt_id?: string;
  status?: PromptEvaluationDimensionScoreStatus;
}

export interface ListPromptEvaluationDimensionScoreTrendsParams {
  asset_id?: string;
  prompt_id?: string;
  status?: PromptEvaluationDimensionScoreStatus;
  since?: string | null;
}

export interface PromptEvaluationSummaryParams {
  since?: string | null;
  include_acceptance_fixtures?: boolean;
}

export interface CreatePromptEvaluationCaseRequest {
  asset_id: string;
  prompt_id?: string | null;
  case_index?: number;
  case_name?: string;
  variables?: Record<string, unknown>;
  expected_contains?: unknown[];
  input?: Record<string, unknown>;
  expected?: Record<string, unknown>;
  tags?: unknown[];
  status?: PromptEvaluationAssetStatus;
}

export interface UpdatePromptEvaluationCaseRequest {
  asset_id?: string;
  prompt_id?: string | null;
  case_index?: number;
  case_name?: string;
  variables?: Record<string, unknown>;
  expected_contains?: unknown[];
  input?: Record<string, unknown>;
  expected?: Record<string, unknown>;
  tags?: unknown[];
  status?: PromptEvaluationAssetStatus;
}

export interface ListPromptEvaluationOptimizationCandidatesParams {
  run_id?: string;
  prompt_id?: string;
  status?: PromptEvaluationOptimizationCandidateStatus;
  limit?: number;
}

export interface CreatePromptEvaluationAssetRequest {
  prompt_id?: string | null;
  name: string;
  description?: string;
  asset_type: PromptEvaluationAssetType;
  payload?: Record<string, unknown>;
  status?: PromptEvaluationAssetStatus;
}

export interface UpdatePromptEvaluationAssetRequest {
  prompt_id?: string | null;
  name?: string;
  description?: string;
  asset_type?: PromptEvaluationAssetType;
  payload?: Record<string, unknown>;
  status?: PromptEvaluationAssetStatus;
}

export interface CreatePromptEvaluationDatasetVersionRequest {
  version_label?: string;
  metadata?: Record<string, unknown>;
}
