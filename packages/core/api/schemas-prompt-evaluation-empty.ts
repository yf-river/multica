import type {
  ListPromptEvaluationAssetsResponse,
  ListPromptEvaluationDatasetVersionsResponse,
  ListPromptEvaluationOptimizationCandidatesResponse,
  PromptEvaluationAsset,
  PromptEvaluationAssetEvidenceSnapshotResponse,
  PromptEvaluationEvidenceSnapshot,
  PromptEvaluationRunEvidence,
  PromptEvaluationStructuredCase,
  PromptEvaluationRun,
  PromptEvaluationOptimizationCandidate,
  PromptEvaluationSkillApplyCandidateResponse,
  PromptEvaluationSkillApplyResult,
  PromptEvaluationSkillFreshnessResult,
  PromptEvaluationSkillReEvalAssetResponse,
  PromptEvaluationSkillReEvalRunResponse,
  ListPromptEvaluationEvidenceSnapshotsResponse,
  PromptEvaluationAssetEvidenceArchivePackage,
  PublishPromptEvaluationOptimizationCandidateResponse,
} from "../types";
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
  dataset_row_count: 0,
  test_suite_case_count: 0,
};

export const EMPTY_PROMPT_EVALUATION_ASSET_LIST_RESPONSE: ListPromptEvaluationAssetsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_DATASET_VERSION_LIST_RESPONSE: ListPromptEvaluationDatasetVersionsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_RUN: PromptEvaluationRun = {
  id: "",
  workspace_id: "",
  asset_id: "",
  prompt_id: null,
  run_kind: "模板渲染检查",
  status: "已入队",
  trigger_source: "手动",
  agent_id: null,
  runtime_id: null,
  task_id: null,
  chat_session_id: null,
  model: "",
  runtime_provider: "",
  total_cases: 0,
  passed_cases: 0,
  failed_cases: 0,
  pass_rate: 0,
  total_duration_ms: 0,
  input_tokens: 0,
  output_tokens: 0,
  estimated_cost: 0,
  failure_reason: "",
  conclusion: "",
  metrics: {},
  evidence: {},
  started_at: "",
  completed_at: "",
  created_by: null,
  created_at: "",
  updated_at: "",
  review_decision: "",
  review_note: "",
  reviewed_at: "",
};

export const EMPTY_PROMPT_EVALUATION_RUN_LIST_RESPONSE = {
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
  created_count: 0,
  skipped_count: 0,
  items: [],
  skipped: [],
};

export const EMPTY_PROMPT_EVALUATION_ASSET_EVIDENCE_ARCHIVE_PACKAGE: PromptEvaluationAssetEvidenceArchivePackage = {
  schema_version: "multica.prompt_evaluation.asset_evidence_archive.v1",
  asset_id: "",
  snapshot_type: "验收归档",
  archived_run_count: 0,
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
  limit: 0,
  offset: 0,
  has_more: false,
  next_cursor: null,
  sort_by: "case_index",
  sort_direction: "asc",
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
  input: {},
  expected: {},
  tags: [],
  status: "启用",
  source: "",
  created_by: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE: PromptEvaluationOptimizationCandidate = {
  id: "",
  workspace_id: "",
  asset_id: "",
  run_id: "",
  prompt_id: "",
  candidate_name: "",
  rationale: "",
  failed_case_count: 0,
  source_prompt_snapshot: {},
  metrics: {},
  skill_patch: null,
  status: "待确认",
  created_by: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_LIST_RESPONSE: ListPromptEvaluationOptimizationCandidatesResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_SKILL_FRESHNESS_RESULT: PromptEvaluationSkillFreshnessResult = {
  status: "stale",
  patch_check: "not_needed",
};

const EMPTY_PROMPT_EVALUATION_SKILL_APPLY_RESULT: PromptEvaluationSkillApplyResult = {
  status: "blocked",
};

export const EMPTY_PROMPT_EVALUATION_SKILL_APPLY_CANDIDATE_RESPONSE: PromptEvaluationSkillApplyCandidateResponse = {
  apply: EMPTY_PROMPT_EVALUATION_SKILL_APPLY_RESULT,
};

export const EMPTY_PROMPT_EVALUATION_SKILL_RE_EVAL_ASSET_RESPONSE: PromptEvaluationSkillReEvalAssetResponse = {
  asset: { id: "" },
  case_count: 0,
};

export const EMPTY_PROMPT_EVALUATION_SKILL_RE_EVAL_RUN_RESPONSE: PromptEvaluationSkillReEvalRunResponse = {
  run: { id: "", status: "已入队" },
};

export const EMPTY_PUBLISH_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_RESPONSE: PublishPromptEvaluationOptimizationCandidateResponse = {
  candidate: EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE,
  prompt: EMPTY_PROMPT_LIBRARY_ITEM,
};
