import type {
  PromptEvaluationRunEvidence,
  PromptEvaluationRun,
  PromptEvaluationAssetEvidenceArchivePackage,
} from "../types";

// Runtime response contracts for prompt evaluation empty.
const EMPTY_PROMPT_EVALUATION_RUN: PromptEvaluationRun = {
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

export const EMPTY_PROMPT_EVALUATION_ASSET_EVIDENCE_ARCHIVE_PACKAGE: PromptEvaluationAssetEvidenceArchivePackage = {
  archived_run_count: 0,
};
