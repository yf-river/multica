import { z } from "zod";
import type {
  PromptEvaluationDatasetExportResponse,
  ImportPromptEvaluationDatasetResponse,
  PromptEvaluationDatasetFromTracesResponse,
} from "../types";
import { PromptEvaluationAssetSchema } from "./schemas-prompt-evaluation-assets";
import { TaskTraceEventSchema } from "./schemas-internal";
import { PromptEvaluationCaseSchema } from "./schemas-prompt-evaluation-case-model";

export {
  PromptEvaluationCaseAssertionSchema,
  PromptEvaluationCaseSchema,
} from "./schemas-prompt-evaluation-case-model";

// Runtime response contracts for prompt evaluation cases.
export const PromptEvaluationCaseListResponseSchema = z.object({
  items: z.array(PromptEvaluationCaseSchema).default([]),
  total: z.number().default(0),
  total_count: z.number().default(0),
  limit: z.number().default(0),
  offset: z.number().default(0),
  has_more: z.boolean().default(false),
  next_cursor: z.string().nullable().optional().transform((v) => v ?? null),
  sort_by: z.enum(["case_index", "case_name", "source", "created_at", "updated_at"]).default("case_index"),
  sort_direction: z.enum(["asc", "desc"]).default("asc"),
}).loose();

export const PromptEvaluationDatasetExportResponseSchema: z.ZodType<PromptEvaluationDatasetExportResponse> = z.object({
  schema: z.literal("multica.prompt_evaluation.dataset_export.v1"),
  exported_at: z.string().default(""),
  source_asset_id: z.string().default(""),
  asset: PromptEvaluationAssetSchema,
  case_count: z.number().default(0),
  cases: z.array(PromptEvaluationCaseSchema).default([]),
  payload: z.record(z.string(), z.unknown()).default({}),
}).loose();

export const ImportPromptEvaluationDatasetResponseSchema: z.ZodType<ImportPromptEvaluationDatasetResponse> = z.object({
  asset: PromptEvaluationAssetSchema,
  source_asset_id: z.string().default(""),
  case_count: z.number().default(0),
  cases: z.array(PromptEvaluationCaseSchema).default([]),
}).loose();

export const PromptEvaluationCaseTagSummarySchema = z.object({
  tag: z.string().default(""),
  case_count: z.number().default(0),
}).loose();

export const PromptEvaluationCaseTagSummaryListResponseSchema = z.object({
  items: z.array(PromptEvaluationCaseTagSummarySchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptEvaluationCaseTagDatasetSummaryDatasetSchema = z.object({
  asset_id: z.string().default(""),
  asset_name: z.string().default(""),
  case_count: z.number().default(0),
}).loose();

export const PromptEvaluationCaseTagDatasetSummarySchema = z.object({
  tag: z.string().default(""),
  case_count: z.number().default(0),
  dataset_count: z.number().default(0),
  top_datasets: z.array(PromptEvaluationCaseTagDatasetSummaryDatasetSchema).default([]),
}).loose();

export const PromptEvaluationCaseTagDatasetSummaryListResponseSchema = z.object({
  items: z.array(PromptEvaluationCaseTagDatasetSummarySchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptEvaluationCaseOperationSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  asset_id: z.string(),
  operation_type: z.string().default(""),
  filter: z.record(z.string(), z.unknown()).default({}),
  input: z.record(z.string(), z.unknown()).default({}),
  changed_count: z.number().default(0),
  skipped_count: z.number().default(0),
  sample_case_ids: z.array(z.unknown()).default([]),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
  status: z.enum(["已入队", "运行中", "已完成", "失败"]).default("已完成"),
  error_message: z.string().default(""),
  started_at: z.string().nullable().optional().transform((v) => v ?? null),
  completed_at: z.string().nullable().optional().transform((v) => v ?? null),
  updated_at: z.string().default(""),
}).loose();

export const PromptEvaluationCaseOperationListResponseSchema = z.object({
  items: z.array(PromptEvaluationCaseOperationSchema).default([]),
  total: z.number().default(0),
}).loose();

export const BulkUpdatePromptEvaluationCaseTagsResponseSchema = z.object({
  operation: PromptEvaluationCaseOperationSchema,
  cases: z.array(PromptEvaluationCaseSchema).default([]),
  changed_count: z.number().default(0),
  skipped_count: z.number().default(0),
}).loose();

export const PromptEvaluationDatasetFromTracesResponseSchema: z.ZodType<PromptEvaluationDatasetFromTracesResponse> = z.object({
  asset: PromptEvaluationAssetSchema,
  cases: z.array(PromptEvaluationCaseSchema).default([]),
  trace_events: z.array(TaskTraceEventSchema).default([]),
  created_count: z.number().default(0),
  skipped_count: z.number().default(0),
  source: z.literal("trace"),
}).loose();
