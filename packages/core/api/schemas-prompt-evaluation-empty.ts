import type {
  PromptEvaluationAsset,
  PromptEvaluationAssetEvidenceSnapshotResponse,
  PromptEvaluationEvidenceSnapshot,
  PromptEvaluationRunEvidence,
  PromptEvaluationStructuredCase,
  PromptEvaluationRun,
  PromptEvaluationOptimizationCandidate,
  PromptEvaluationSkillFreshnessResult,
  PromptEvaluationAssetEvidenceArchivePackage,
} from "../types";

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
  updated_at: "",
  dataset_row_count: 0,
  test_suite_case_count: 0,
};

export const EMPTY_PROMPT_EVALUATION_RUN: PromptEvaluationRun = {
  id: "",
  asset_id: "",
  prompt_id: null,
  run_kind: "模板渲染检查",
  status: "已入队",
  task_id: null,
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
  created_at: "",
  review_decision: "",
  review_note: "",
  reviewed_at: "",
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
  run_id: "",
  snapshot_type: "手动归档",
  summary: {},
  created_at: "",
};

export const EMPTY_PROMPT_EVALUATION_ASSET_EVIDENCE_SNAPSHOT_RESPONSE: PromptEvaluationAssetEvidenceSnapshotResponse = {
  created_count: 0,
  skipped_count: 0,
  items: [],
};

export const EMPTY_PROMPT_EVALUATION_ASSET_EVIDENCE_ARCHIVE_PACKAGE: PromptEvaluationAssetEvidenceArchivePackage = {
  archived_run_count: 0,
};

export const EMPTY_PROMPT_EVALUATION_CASE: PromptEvaluationStructuredCase = {
  id: "",
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
  updated_at: "",
};

export const EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE: PromptEvaluationOptimizationCandidate = {
  id: "",
  run_id: "",
  candidate_name: "",
  rationale: "",
  failed_case_count: 0,
  source_prompt_snapshot: {},
  metrics: {},
  skill_patch: null,
  status: "待确认",
};

export const EMPTY_PROMPT_EVALUATION_SKILL_FRESHNESS_RESULT: PromptEvaluationSkillFreshnessResult = {
  status: "stale",
  patch_check: "not_needed",
};
