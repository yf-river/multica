import { z } from "zod";
import { PromptEvaluationAssetSchema } from "./schemas-prompt-evaluation-assets";
import { NonEmptyStringSchema, TaskTraceEventSchema } from "./schemas-internal";

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
  reviewed_at: z.string().default(""),
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

const PromptEvaluationToolCallChainSchema = z.object({
  id: z.string(),
  task_id: z.string().optional(),
  tool: z.string().optional(),
  status: z.string().default(""),
  use_seq: z.number().optional(),
  result_seq: z.number().optional(),
  input: z.record(z.string(), z.unknown()).optional(),
  output: z.string().optional(),
  duration_ms: z.number().optional(),
  failure_signal: z.boolean().default(false),
  failure_reason: z.string().optional(),
  summary: z.string().default(""),
  created_at: z.string().optional(),
  completed_at: z.string().optional(),
}).loose();

export const PromptEvaluationRunEvidenceSchema = z.object({
  run: PromptEvaluationRunSchema,
  trials: z.array(z.record(z.string(), z.unknown())).default([]),
  task_usage: z.array(z.record(z.string(), z.unknown())).default([]),
  task_messages: z.array(PromptEvaluationTaskMessageSchema).default([]),
  trace_events: z.array(TaskTraceEventSchema).default([]),
  execution_spans: z.array(z.record(z.string(), z.unknown())).default([]),
  tool_call_chains: z.array(PromptEvaluationToolCallChainSchema).default([]),
  tool_call_summary: z.array(z.record(z.string(), z.unknown())).default([]),
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
}).loose().transform(({ items }) => items);

export const PromptEvaluationAssetEvidenceSnapshotResponseSchema = z.object({
  asset_id: NonEmptyStringSchema,
  snapshot_type: z.enum(["手动归档", "验收归档", "自动归档"]).default("验收归档"),
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
  asset_id: z.string().default(""),
  snapshot_type: z.enum(["手动归档", "验收归档", "自动归档"]).default("验收归档"),
  archived_run_count: z.number().default(0),
  asset: PromptEvaluationAssetSchema,
  items: z.array(z.object({
    run: PromptEvaluationRunSchema,
    snapshots: z.array(PromptEvaluationEvidenceSnapshotSchema).default([]),
  }).loose()).default([]),
  中文摘要: z.record(z.string(), z.unknown()).default({}),
}).loose();

export const PromptEvaluationRunListResponseSchema = z.object({
  items: z.array(PromptEvaluationRunSchema).default([]),
}).loose().transform(({ items }) => items);
