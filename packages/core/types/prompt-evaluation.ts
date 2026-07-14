import type { PromptLibraryItem } from "./prompt-library";
import type { TaskMessagePayload } from "./events";
import type { TaskTraceEvent } from "./agent";

export type PromptEvaluationAssetType = "数据集" | "测试套件";
export type PromptEvaluationAssetStatus = "启用" | "归档";
export type PromptEvaluationCaseStatus = PromptEvaluationAssetStatus | "draft" | "approved" | "active";
export type PromptEvaluationCaseSortBy = "case_index" | "case_name" | "source" | "created_at" | "updated_at";
export type PromptEvaluationOptimizationCandidateStatus = "待确认" | "已发布" | "已拒绝";

export interface PromptEvaluationAsset {
  id: string;
  workspace_id: string;
  prompt_id: string | null;
  name: string;
  description: string;
  asset_type: PromptEvaluationAssetType;
  payload: Record<string, unknown>;
  status: PromptEvaluationAssetStatus;
  created_by: string | null;
  created_at: string;
  updated_at: string;
  dataset_row_count: number;
  test_suite_case_count: number;
}

export interface PromptEvaluationDatasetVersion {
  id: string;
  version: number;
  version_label: string;
  row_count: number;
  row_fingerprint: string;
  created_at: string;
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
  reviewed_at: string;
}

export interface PromptEvaluationToolCallChain {
  id: string;
  task_id?: string;
  tool?: string;
  status: "已配对" | "缺少结果" | "孤立结果" | string;
  use_seq?: number;
  result_seq?: number;
  input?: Record<string, unknown>;
  output?: string;
  duration_ms?: number;
  failure_signal: boolean;
  failure_reason?: string;
  summary: string;
  created_at?: string;
  completed_at?: string;
}

export interface PromptEvaluationRunEvidence {
  run: PromptEvaluationRun;
  trials: Array<Record<string, unknown>>;
  task_usage: Array<Record<string, unknown>>;
  task_messages: TaskMessagePayload[];
  trace_events: TaskTraceEvent[];
  execution_spans: Record<string, unknown>[];
  tool_call_chains: PromptEvaluationToolCallChain[];
  tool_call_summary: Record<string, unknown>[];
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

export interface PromptEvaluationAssetEvidenceSnapshotSkip {
  run_id: string;
  reason: string;
}

export interface PromptEvaluationAssetEvidenceSnapshotResponse {
  asset_id: string;
  snapshot_type: PromptEvaluationEvidenceSnapshotType;
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
  asset_id: string;
  snapshot_type: PromptEvaluationEvidenceSnapshotType;
  archived_run_count: number;
  asset: PromptEvaluationAsset;
  items: PromptEvaluationAssetEvidenceArchiveItem[];
  中文摘要: Record<string, unknown>;
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
  status: PromptEvaluationCaseStatus;
  source: string;
  created_by: string | null;
  created_at: string;
  updated_at: string;
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
  run_id: string;
  candidate_name: string;
  rationale: string;
  failed_case_count: number;
  source_prompt_snapshot: Record<string, unknown>;
  metrics: Record<string, unknown>;
  skill_patch?: PromptEvaluationSkillPatch | null;
  status: PromptEvaluationOptimizationCandidateStatus;
}

export interface PromptEvaluationSkillPatch {
  schema_version: "multica.skill.patch.v1" | string;
  patch: string;
  patch_hash: string;
  candidate_intent: "update_existing_skill" | "create_operation_skill" | string;
  operation_skill_key?: string;
  operation_skill_path?: string;
  operation_skill_reason?: string;
  source_snapshot?: Record<string, unknown>;
  source_resource_id?: string;
  repo_path?: string;
  target_branch?: string;
  skill_path?: string;
  changelog_path?: string;
  expected_improvement?: string;
  risk?: string;
  verification_plan?: string;
  publication_status: string;
  created_at?: string;
  updated_at?: string;
}

export interface CheckPromptEvaluationSkillFreshnessRequest {
  source_resource_id?: string;
  repo_path?: string;
  target_branch?: string;
  skill_path?: string;
}

export interface PromptEvaluationSkillFreshnessResult {
  status: "fresh" | "branch_changed_skill_unchanged" | "stale" | "conflict" | "rebaseable";
  patch_check: "not_needed" | "missing_patch" | "conflict" | "applies" | "creates_file" | "target_exists" | string;
}

export interface ApplyPromptEvaluationSkillCandidateRequest {
  source_resource_id?: string;
  repo_path?: string;
  target_branch?: string;
  skill_path?: string;
  changelog_path?: string;
  allow_dirty?: boolean;
  skip_changelog?: boolean;
}

export interface PromptEvaluationSkillApplyResult {
  status: "dry_run" | "applied" | "blocked" | "conflict";
}

export interface PromptEvaluationSkillApplyCandidateResponse {
  apply: PromptEvaluationSkillApplyResult;
}

export interface PreparePromptEvaluationSkillReEvalRequest {
  source_resource_id?: string;
  repo_path?: string;
  target_branch?: string;
  skill_path?: string;
  include_draft?: boolean;
}

export interface RunPromptEvaluationSkillReEvalRequest {
  asset_id?: string;
}

export interface PromptEvaluationSkillReEvalAssetResponse {
  asset: Pick<PromptEvaluationAsset, "id">;
  case_count: number;
}

export interface PromptEvaluationSkillReEvalRunResponse {
  run: Pick<PromptEvaluationRun, "id" | "status">;
}

export interface PublishPromptEvaluationOptimizationCandidateResponse {
  candidate: PromptEvaluationOptimizationCandidate;
  prompt: PromptLibraryItem;
}

export interface RejectPromptEvaluationOptimizationCandidateRequest {
  reason?: string;
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
  offset?: number;
}

export interface ReviewPromptEvaluationRunRequest {
  decision: "通过" | "未通过";
  note?: string;
}

export interface ListPromptEvaluationCasesParams {
  asset_id?: string;
  status?: PromptEvaluationCaseStatus;
  source?: "manual" | "trace" | "payload";
  tag?: string;
  keyword?: string;
  limit?: number;
  cursor?: string;
  sort_by?: PromptEvaluationCaseSortBy;
  sort_direction?: "asc" | "desc";
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
  status?: PromptEvaluationCaseStatus;
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
  status?: PromptEvaluationCaseStatus;
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
