import { z } from "zod";
import { PromptEvaluationAssetSchema } from "./schemas-prompt-evaluation-assets";
import { NonEmptyStringSchema, PromptEvaluationTaskTraceEventSchema } from "./schemas-internal";

// Runtime response contracts for prompt evaluation runs.
export const PromptEvaluationRunSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  asset_id: NonEmptyStringSchema,
  prompt_id: z.string().nullable().optional().transform((v) => v ?? null),
  run_kind: z.enum(["本地渲染", "模板渲染检查", "Agent执行"]).transform((value) => (value === "本地渲染" ? "模板渲染检查" : value)),
  status: z.enum(["已入队", "运行中", "通过", "未通过", "失败", "已取消", "需人工复核"]),
  trigger_source: z.string().default("手动"),
  agent_id: z.string().nullable().optional().transform((v) => v ?? null),
  runtime_id: z.string().nullable().optional().transform((v) => v ?? null),
  task_id: z.string().nullable().optional().transform((v) => v ?? null),
  chat_session_id: z.string().nullable().optional().transform((v) => v ?? null),
  model: z.string().default(""),
  runtime_provider: z.string().default(""),
  total_cases: z.number().default(0),
  passed_cases: z.number().default(0),
  failed_cases: z.number().default(0),
  pass_rate: z.number().default(0),
  total_duration_ms: z.number().default(0),
  average_duration_ms: z.number().default(0),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  estimated_cost: z.number().default(0),
  failure_reason: z.string().default(""),
  conclusion: z.string().default(""),
  metrics: z.record(z.string(), z.unknown()).default({}),
  evidence: z.record(z.string(), z.unknown()).default({}),
  started_at: z.string().default(""),
  completed_at: z.string().default(""),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
  review_decision: z.enum(["", "通过", "未通过"]).default(""),
  review_note: z.string().default(""),
  reviewed_by: z.string().nullable().optional().transform((v) => v ?? null),
  reviewed_at: z.string().default(""),
}).loose();

export const PromptEvaluationAgentRunResponseSchema = z.object({
  asset: PromptEvaluationAssetSchema,
  run: PromptEvaluationRunSchema,
  task_id: NonEmptyStringSchema,
  chat_session_id: NonEmptyStringSchema,
  agent_id: NonEmptyStringSchema,
  runtime_id: NonEmptyStringSchema,
  model: z.string(),
  status: z.string(),
  message: z.string(),
}).loose();

export const PromptEvaluationTrialSchema = z.object({
  id: z.string(),
  run_id: z.string(),
  workspace_id: z.string(),
  asset_id: z.string(),
  case_index: z.number().default(0),
  case_name: z.string().default(""),
  status: z.enum(["待执行", "通过", "未通过", "失败", "已跳过", "需人工复核"]),
  input: z.record(z.string(), z.unknown()).default({}),
  expected: z.record(z.string(), z.unknown()).default({}),
  output: z.unknown().default({}),
  rendered_prompt: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  duration_ms: z.number().default(0),
  failure_reason: z.string().default(""),
  evidence: z.record(z.string(), z.unknown()).default({}),
  created_at: z.string().default(""),
}).loose();

const PromptEvaluationTaskUsageSchema = z.object({
  id: z.string(),
  task_id: z.string(),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  estimated_cost: z.number().optional(),
  priced: z.boolean().optional(),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

const PromptEvaluationTaskMessageSchema = z.object({
  task_id: z.string(),
  issue_id: z.string().default(""),
  chat_session_id: z.string().optional(),
  seq: z.number().default(0),
  type: z.enum(["text", "thinking", "tool_use", "tool_result", "error"]),
  tool: z.string().optional(),
  content: z.string().optional(),
  input: z.record(z.string(), z.unknown()).optional(),
  output: z.string().optional(),
  created_at: z.string().optional(),
}).loose();

const PromptEvaluationExecutionSpanSchema = z.object({
  id: z.string(),
  parent_id: z.string().optional(),
  span_kind: z.string().default(""),
  span_name: z.string().default(""),
  status: z.string().default(""),
  seq: z.number().default(0),
  task_id: z.string().optional(),
  tool: z.string().optional(),
  provider: z.string().optional(),
  model: z.string().optional(),
  token_total: z.number().default(0),
  duration_ms: z.number().default(0),
  summary: z.string().default(""),
  details: z.record(z.string(), z.unknown()).optional(),
  created_at: z.string().optional(),
}).loose();

const PromptEvaluationToolCallChainSchema = z.object({
  id: z.string(),
  task_id: z.string().optional(),
  tool: z.string().optional(),
  status: z.string().default(""),
  use_seq: z.number().optional(),
  result_seq: z.number().optional(),
  use_span_id: z.string().optional(),
  result_span_id: z.string().optional(),
  input: z.record(z.string(), z.unknown()).optional(),
  output: z.string().optional(),
  duration_ms: z.number().optional(),
  result_category: z.string().optional(),
  failure_signal: z.boolean().default(false),
  failure_reason: z.string().optional(),
  summary: z.string().default(""),
  created_at: z.string().optional(),
  completed_at: z.string().optional(),
}).loose();

const PromptEvaluationToolCallSummarySchema = z.object({
  tool: z.string(),
  total_calls: z.number().default(0),
  paired_calls: z.number().default(0),
  missing_result_calls: z.number().default(0),
  orphan_result_calls: z.number().default(0),
  average_duration_ms: z.number().optional(),
  max_duration_ms: z.number().optional(),
  slowest_tool_call_chain_id: z.string().optional(),
  result_categories: z.record(z.string(), z.number()).optional(),
  failure_signal_calls: z.number().default(0),
  needs_attention: z.boolean().default(false),
  summary: z.string().default(""),
}).loose();

export const PromptEvaluationRunEvidenceSchema = z.object({
  run: PromptEvaluationRunSchema,
  trials: z.array(PromptEvaluationTrialSchema).default([]),
  task_usage: z.array(PromptEvaluationTaskUsageSchema).default([]),
  task_messages: z.array(PromptEvaluationTaskMessageSchema).default([]),
  trace_events: z.array(PromptEvaluationTaskTraceEventSchema).default([]),
  execution_spans: z.array(PromptEvaluationExecutionSpanSchema).default([]),
  tool_call_chains: z.array(PromptEvaluationToolCallChainSchema).default([]),
  tool_call_summary: z.array(PromptEvaluationToolCallSummarySchema).default([]),
  execution_summary: z.record(z.string(), z.unknown()).default({}),
  evidence: z.record(z.string(), z.unknown()).default({}),
  上下文: z.record(z.string(), z.unknown()).default({}),
}).loose();

export const PromptEvaluationEvidenceSnapshotSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  run_id: NonEmptyStringSchema,
  snapshot_type: z.enum(["手动归档", "验收归档", "自动归档"]).default("手动归档"),
  schema_version: z.string().default("multica.prompt_evaluation.evidence_snapshot.v1"),
  summary: z.record(z.string(), z.unknown()).default({}),
  evidence: z.record(z.string(), z.unknown()).optional(),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
}).loose();

export const PromptEvaluationEvidenceSnapshotListResponseSchema = z.object({
  items: z.array(PromptEvaluationEvidenceSnapshotSchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptEvaluationAssetEvidenceSnapshotResponseSchema = z.object({
  asset_id: NonEmptyStringSchema,
  snapshot_type: z.enum(["手动归档", "验收归档", "自动归档"]).default("验收归档"),
  total_runs: z.number().default(0),
  created_count: z.number().default(0),
  skipped_count: z.number().default(0),
  items: z.array(PromptEvaluationEvidenceSnapshotSchema).default([]),
  skipped: z.array(z.object({
    run_id: z.string().default(""),
    reason: z.string().default(""),
  }).loose()).default([]),
}).loose();

export const PromptEvaluationAssetEvidenceArchivePackageSchema = z.object({
  schema_version: z.string().default("multica.prompt_evaluation.asset_evidence_archive.v1"),
  generated_at: z.string().default(""),
  asset_id: z.string().default(""),
  snapshot_type: z.enum(["手动归档", "验收归档", "自动归档"]).default("验收归档"),
  total_runs: z.number().default(0),
  archived_run_count: z.number().default(0),
  missing_run_count: z.number().default(0),
  asset: PromptEvaluationAssetSchema,
  items: z.array(z.object({
    run: PromptEvaluationRunSchema,
    snapshots: z.array(PromptEvaluationEvidenceSnapshotSchema).default([]),
  }).loose()).default([]),
  中文摘要: z.record(z.string(), z.unknown()).default({}),
}).loose();

export const PromptEvaluationRunListResponseSchema = z.object({
  items: z.array(PromptEvaluationRunSchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptEvaluationTrialListResponseSchema = z.object({
  items: z.array(PromptEvaluationTrialSchema).default([]),
  total: z.number().default(0),
}).loose();
