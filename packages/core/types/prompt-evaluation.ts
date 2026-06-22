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

export interface PromptEvaluationRunEvidence {
  run: PromptEvaluationRun;
  trials: PromptEvaluationTrial[];
  task_usage: PromptEvaluationTaskUsage[];
  task_messages: TaskMessagePayload[];
  trace_events: TaskTraceEvent[];
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
  input: Record<string, unknown>;
  expected: Record<string, unknown>;
  tags: unknown[];
  status: PromptEvaluationAssetStatus;
  source: string;
  created_by: string | null;
  created_at: string;
  updated_at: string;
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

export interface ListPromptEvaluationCasesParams {
  asset_id?: string;
  status?: PromptEvaluationAssetStatus;
}

export interface PromptEvaluationSummaryParams {
  since?: string | null;
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
