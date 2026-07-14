import { z } from "zod";
import type { PromptEvaluationDatasetFromTracesResponse } from "../types";
import { PromptEvaluationAssetSchema } from "./schemas-prompt-evaluation-assets";
import { TaskTraceEventSchema } from "./schemas-internal";
import { PromptEvaluationCaseSchema } from "./schemas-prompt-evaluation-case-model";

export { PromptEvaluationCaseSchema } from "./schemas-prompt-evaluation-case-model";

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

export const PromptEvaluationDatasetFromTracesResponseSchema: z.ZodType<PromptEvaluationDatasetFromTracesResponse> = z.object({
  asset: PromptEvaluationAssetSchema,
  cases: z.array(PromptEvaluationCaseSchema).default([]),
  trace_events: z.array(TaskTraceEventSchema).default([]),
  created_count: z.number().default(0),
  skipped_count: z.number().default(0),
  source: z.literal("trace"),
}).loose();
