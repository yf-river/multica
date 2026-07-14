import { z } from "zod";
import type { PromptEvaluationDatasetFromTracesResponse } from "../types";
import { PromptEvaluationAssetSchema } from "./schemas-prompt-evaluation-assets";
import { TaskTraceEventSchema } from "./schemas-internal";
import { PromptEvaluationCaseSchema } from "./schemas-prompt-evaluation-case-model";

export { PromptEvaluationCaseSchema } from "./schemas-prompt-evaluation-case-model";

// Runtime response contracts for prompt evaluation cases.
export const PromptEvaluationCaseListResponseSchema = z.object({
  items: z.array(PromptEvaluationCaseSchema).default([]),
}).loose().transform(({ items }) => items);

export const PromptEvaluationDatasetFromTracesResponseSchema: z.ZodType<PromptEvaluationDatasetFromTracesResponse> = z.object({
  asset: PromptEvaluationAssetSchema,
  cases: z.array(PromptEvaluationCaseSchema).default([]),
  trace_events: z.array(TaskTraceEventSchema).default([]),
  created_count: z.number().default(0),
  skipped_count: z.number().default(0),
  source: z.literal("trace"),
}).loose();
