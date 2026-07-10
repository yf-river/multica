import type {
  ListPromptEvaluationAssetsResponse,
  ListPromptEvaluationDatasetVersionRowsResponse,
  ListPromptEvaluationDatasetVersionTagTrendsResponse,
  ListPromptEvaluationDatasetVersionsResponse,
  ListPromptEvaluationOptimizationCandidatesResponse,
  ListPromptEvaluationCaseTagSummariesResponse,
  ListPromptEvaluationCaseTagDatasetSummariesResponse,
  ListPromptEvaluationCaseOperationsResponse,
  PromptEvaluationAsset,
  PromptEvaluationAssetEvidenceSnapshotResponse,
  PromptEvaluationEvidenceSnapshot,
  PromptEvaluationRunEvidence,
  PromptEvaluationStructuredCase,
  PromptEvaluationCaseOperation,
  BulkUpdatePromptEvaluationCaseTagsResponse,
  PromptEvaluationDatasetVersionDiff,
  RestorePromptEvaluationDatasetVersionResponse,
  PromptEvaluationDimensionScore,
  ListPromptEvaluationDimensionScoresResponse,
  PromptEvaluationDimensionScoreSummary,
  ListPromptEvaluationDimensionScoreSummariesResponse,
  PromptEvaluationDimensionScoreTrend,
  ListPromptEvaluationDimensionScoreTrendsResponse,
  PromptEvaluationOptimizationCandidate,
  PromptEvaluationSkillApplyCandidateResponse,
  PromptEvaluationSkillApplyResult,
  PromptEvaluationSkillCaseDraftsResult,
  PromptEvaluationSkillFreshnessResult,
  PromptEvaluationSkillInventoryResponse,
  PromptEvaluationSkillInventoryResult,
  PromptEvaluationSkillReEvalAssetResponse,
  PromptEvaluationSkillReEvalRunResponse,
  PromptEvaluationSkillSnapshotResult,
  ListPromptEvaluationEvidenceSnapshotsResponse,
  PromptEvaluationAssetEvidenceArchivePackage,
  PublishPromptEvaluationOptimizationCandidateResponse,
} from "../types";
import { PromptEvaluationDatasetVersionSchema } from "./schemas-prompt-evaluation-assets";
import { PromptEvaluationRunSchema } from "./schemas-prompt-evaluation-runs";
import { EMPTY_PROMPT_LIBRARY_ITEM } from "./schemas-prompt-library";

// Runtime response contracts for prompt evaluation empty.
export const EMPTY_PROMPT_EVALUATION_ASSET: PromptEvaluationAsset = {
  id: "",
  workspace_id: "",
  prompt_id: null,
  name: "",
  description: "",
  asset_type: "数据集",
  payload: {},
  status: "启用",
  created_by: null,
  created_at: "",
  updated_at: "",
  structure_schema: "multica.training_evaluation.asset_profile.v1",
  structured_case_count: 0,
  structured_variable_count: 0,
  structured_assertion_count: 0,
  linked_dataset_count: 0,
  linked_prompt_count: 0,
  evaluation_dimension_count: 0,
  dataset_row_count: 0,
  test_suite_case_count: 0,
  experiment_dimension_count: 0,
};

export const EMPTY_PROMPT_EVALUATION_ASSET_LIST_RESPONSE: ListPromptEvaluationAssetsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_DATASET_VERSION_LIST_RESPONSE: ListPromptEvaluationDatasetVersionsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_DATASET_VERSION_ROW_LIST_RESPONSE: ListPromptEvaluationDatasetVersionRowsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_DATASET_VERSION_TAG_TREND_LIST_RESPONSE: ListPromptEvaluationDatasetVersionTagTrendsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_DATASET_VERSION_DIFF: PromptEvaluationDatasetVersionDiff = {
  base_version: PromptEvaluationDatasetVersionSchema.parse({
    id: "",
    workspace_id: "",
    dataset_asset_id: "",
  }),
  target_version: PromptEvaluationDatasetVersionSchema.parse({
    id: "",
    workspace_id: "",
    dataset_asset_id: "",
  }),
  summary: {},
  added: [],
  removed: [],
  changed: [],
  unchanged: [],
};

export const EMPTY_PROMPT_EVALUATION_RUN = PromptEvaluationRunSchema.parse({
  id: "",
  workspace_id: "",
  asset_id: "",
  run_kind: "模板渲染检查",
  status: "已入队",
});

export const EMPTY_PROMPT_EVALUATION_RUN_LIST_RESPONSE = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_TRIAL_LIST_RESPONSE = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_RUN_EVIDENCE: PromptEvaluationRunEvidence = {
  run: EMPTY_PROMPT_EVALUATION_RUN,
  trials: [],
  task_usage: [],
  task_messages: [],
  trace_events: [],
  execution_spans: [],
  tool_call_chains: [],
  tool_call_summary: [],
  execution_summary: {},
  evidence: {},
  上下文: {},
};

export const EMPTY_PROMPT_EVALUATION_EVIDENCE_SNAPSHOT: PromptEvaluationEvidenceSnapshot = {
  id: "",
  workspace_id: "",
  run_id: "",
  snapshot_type: "手动归档",
  schema_version: "multica.prompt_evaluation.evidence_snapshot.v1",
  summary: {},
  evidence: {},
  created_by: null,
  created_at: "",
};

export const EMPTY_PROMPT_EVALUATION_ASSET_EVIDENCE_SNAPSHOT_RESPONSE: PromptEvaluationAssetEvidenceSnapshotResponse = {
  asset_id: "",
  snapshot_type: "验收归档",
  total_runs: 0,
  created_count: 0,
  skipped_count: 0,
  items: [],
  skipped: [],
};

export const EMPTY_PROMPT_EVALUATION_ASSET_EVIDENCE_ARCHIVE_PACKAGE: PromptEvaluationAssetEvidenceArchivePackage = {
  schema_version: "multica.prompt_evaluation.asset_evidence_archive.v1",
  generated_at: "",
  asset_id: "",
  snapshot_type: "验收归档",
  total_runs: 0,
  archived_run_count: 0,
  missing_run_count: 0,
  asset: EMPTY_PROMPT_EVALUATION_ASSET,
  items: [],
  中文摘要: {},
};

export const EMPTY_PROMPT_EVALUATION_EVIDENCE_SNAPSHOT_LIST_RESPONSE: ListPromptEvaluationEvidenceSnapshotsResponse = {
  items: [],
  total: 0,
};
export const EMPTY_PROMPT_EVALUATION_CASE_LIST_RESPONSE = {
  items: [],
  total: 0,
  total_count: 0,
  limit: 0,
  offset: 0,
  has_more: false,
  next_cursor: null,
  sort_by: "case_index",
  sort_direction: "asc",
};

export const EMPTY_PROMPT_EVALUATION_CASE_TAG_SUMMARY_LIST_RESPONSE: ListPromptEvaluationCaseTagSummariesResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_CASE_TAG_DATASET_SUMMARY_LIST_RESPONSE: ListPromptEvaluationCaseTagDatasetSummariesResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_CASE_OPERATION: PromptEvaluationCaseOperation = {
  id: "",
  workspace_id: "",
  asset_id: "",
  operation_type: "",
  filter: {},
  input: {},
  changed_count: 0,
  skipped_count: 0,
  sample_case_ids: [],
  created_by: null,
  created_at: "",
  status: "已完成",
  error_message: "",
  started_at: null,
  completed_at: null,
  updated_at: "",
};

export const EMPTY_PROMPT_EVALUATION_CASE_OPERATION_LIST_RESPONSE: ListPromptEvaluationCaseOperationsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_BULK_PROMPT_EVALUATION_CASE_TAGS_RESPONSE: BulkUpdatePromptEvaluationCaseTagsResponse = {
  operation: EMPTY_PROMPT_EVALUATION_CASE_OPERATION,
  cases: [],
  changed_count: 0,
  skipped_count: 0,
};

export const EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE_LIST_RESPONSE: ListPromptEvaluationDimensionScoresResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE_SUMMARY_LIST_RESPONSE: ListPromptEvaluationDimensionScoreSummariesResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE_TREND_LIST_RESPONSE: ListPromptEvaluationDimensionScoreTrendsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE: PromptEvaluationDimensionScore = {
  id: "",
  workspace_id: "",
  run_id: "",
  asset_id: "",
  prompt_id: null,
  dimension_index: 0,
  dimension_name: "",
  score: 0,
  passed_cases: 0,
  total_cases: 0,
  status: "待执行",
  rule: "",
  evidence: "",
  source: "",
  created_at: "",
  updated_at: "",
};

export const EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE_SUMMARY: PromptEvaluationDimensionScoreSummary = {
  workspace_id: "",
  asset_id: "",
  prompt_id: null,
  dimension_index: 0,
  dimension_name: "",
  run_count: 0,
  scored_run_count: 0,
  passed_cases: 0,
  total_cases: 0,
  score: 0,
  latest_status: "待执行",
  latest_rule: "",
  latest_evidence: "",
  latest_source: "",
  latest_scored_at: "",
};

export const EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE_TREND: PromptEvaluationDimensionScoreTrend = {
  workspace_id: "",
  asset_id: "",
  prompt_id: null,
  dimension_index: 0,
  dimension_name: "",
  period: "",
  prompt_version: 0,
  run_count: 0,
  scored_run_count: 0,
  passed_cases: 0,
  total_cases: 0,
  score: 0,
  latest_status: "待执行",
  latest_rule: "",
  latest_evidence: "",
  latest_source: "",
  latest_scored_at: "",
};

export const EMPTY_PROMPT_EVALUATION_CASE: PromptEvaluationStructuredCase = {
  id: "",
  workspace_id: "",
  asset_id: "",
  prompt_id: null,
  case_index: 0,
  case_name: "",
  variables: {},
  expected_contains: [],
  assertions: [],
  input: {},
  expected: {},
  tags: [],
  status: "启用",
  source: "",
  created_by: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_RESTORE_PROMPT_EVALUATION_DATASET_VERSION_RESPONSE: RestorePromptEvaluationDatasetVersionResponse = {
  asset: EMPTY_PROMPT_EVALUATION_ASSET,
  restored_from: EMPTY_PROMPT_EVALUATION_DATASET_VERSION_DIFF.base_version,
  restored_version: EMPTY_PROMPT_EVALUATION_DATASET_VERSION_DIFF.target_version,
  restored_cases: [],
};

export const EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE: PromptEvaluationOptimizationCandidate = {
  id: "",
  workspace_id: "",
  asset_id: "",
  run_id: "",
  prompt_id: "",
  candidate_name: "",
  candidate_content: "",
  rationale: "",
  failed_case_count: 0,
  source_failure_summary: {},
  source_prompt_snapshot: {},
  metrics: {},
  skill_patch: null,
  status: "待确认",
  published_prompt_id: null,
  published_at: "",
  created_by: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_LIST_RESPONSE: ListPromptEvaluationOptimizationCandidatesResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_SKILL_INVENTORY_RESULT: PromptEvaluationSkillInventoryResult = {
  schema_version: "multica.skill.inventory.v1",
  provider: "gongfeng",
  repo: "",
  branch: "",
  head_commit: "",
  skill_root: ".codebuddy/skills",
  items: [],
  discovered_count: 0,
  snapshot_time: "",
};

export const EMPTY_PROMPT_EVALUATION_SKILL_INVENTORY_RESPONSE: PromptEvaluationSkillInventoryResponse = {
  asset: EMPTY_PROMPT_EVALUATION_ASSET,
  inventory: EMPTY_PROMPT_EVALUATION_SKILL_INVENTORY_RESULT,
};

export const EMPTY_PROMPT_EVALUATION_SKILL_SNAPSHOT_RESULT: PromptEvaluationSkillSnapshotResult = {
  asset: EMPTY_PROMPT_EVALUATION_ASSET,
  snapshot: {
    schema_version: "multica.skill.snapshot.v1",
    provider: "gongfeng",
    repo: "",
    branch: "",
    base_commit: "",
    skill_path: "",
    skill_hash: "",
    snapshot_time: "",
  },
};

export const EMPTY_PROMPT_EVALUATION_SKILL_CASE_DRAFTS_RESULT: PromptEvaluationSkillCaseDraftsResult = {
  asset: EMPTY_PROMPT_EVALUATION_ASSET,
  drafts: [],
  created_count: 0,
};

export const EMPTY_PROMPT_EVALUATION_SKILL_FRESHNESS_RESULT: PromptEvaluationSkillFreshnessResult = {
  schema_version: "multica.skill.freshness.v1",
  status: "stale",
  reason: "",
  target_branch: "",
  head_commit: "",
  base_commit: "",
  skill_path: "",
  base_skill_hash: "",
  current_skill_hash: "",
  patch_check: "not_needed",
  checked_at: "",
  snapshot: EMPTY_PROMPT_EVALUATION_SKILL_SNAPSHOT_RESULT.snapshot,
};

export const EMPTY_PROMPT_EVALUATION_SKILL_APPLY_RESULT: PromptEvaluationSkillApplyResult = {
  schema_version: "multica.skill.apply.v1",
  status: "blocked",
  reason: "",
  repo_path: "",
  target_branch: "",
  head_commit: "",
  skill_path: "",
  skill_hash_before: "",
  skill_hash_after: "",
  patch_check: "not_run",
  freshness: EMPTY_PROMPT_EVALUATION_SKILL_FRESHNESS_RESULT,
  changed_files: [],
  re_eval_required: true,
  re_eval_plan: {},
  checked_at: "",
  snapshot: EMPTY_PROMPT_EVALUATION_SKILL_SNAPSHOT_RESULT.snapshot,
};

export const EMPTY_PROMPT_EVALUATION_SKILL_APPLY_CANDIDATE_RESPONSE: PromptEvaluationSkillApplyCandidateResponse = {
  candidate: EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE,
  apply: EMPTY_PROMPT_EVALUATION_SKILL_APPLY_RESULT,
};

export const EMPTY_PROMPT_EVALUATION_SKILL_RE_EVAL_ASSET_RESPONSE: PromptEvaluationSkillReEvalAssetResponse = {
  candidate: EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE,
  asset: EMPTY_PROMPT_EVALUATION_ASSET,
  source_snapshot: EMPTY_PROMPT_EVALUATION_SKILL_SNAPSHOT_RESULT.snapshot,
  re_eval_snapshot: EMPTY_PROMPT_EVALUATION_SKILL_SNAPSHOT_RESULT.snapshot,
  case_count: 0,
  cases: [],
  re_eval_plan: {},
};

export const EMPTY_PROMPT_EVALUATION_SKILL_RE_EVAL_RUN_RESPONSE: PromptEvaluationSkillReEvalRunResponse = {
  candidate: EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE,
  asset: EMPTY_PROMPT_EVALUATION_ASSET,
  run: EMPTY_PROMPT_EVALUATION_RUN,
  source_snapshot: EMPTY_PROMPT_EVALUATION_SKILL_SNAPSHOT_RESULT.snapshot,
  re_eval_snapshot: EMPTY_PROMPT_EVALUATION_SKILL_SNAPSHOT_RESULT.snapshot,
  case_count: 0,
  proof_scope: "local_prompt_evaluation_run",
  re_eval_run: {},
};

export const EMPTY_PUBLISH_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_RESPONSE: PublishPromptEvaluationOptimizationCandidateResponse = {
  candidate: EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE,
  prompt: EMPTY_PROMPT_LIBRARY_ITEM,
};
