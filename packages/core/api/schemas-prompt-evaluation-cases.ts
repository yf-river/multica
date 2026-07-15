import { z } from "zod";
import type { PromptEvaluationDatasetFromTracesResponse } from "../types";
import { PromptEvaluationCaseSchema } from "./schemas-prompt-evaluation-case-model";

export {
  PromptEvaluationCaseMutationResultSchema,
  PromptEvaluationCaseSchema,
} from "./schemas-prompt-evaluation-case-model";

// Runtime response contracts for prompt evaluation cases.
export const PromptEvaluationCaseListResponseSchema = z.object({
  items: z.array(PromptEvaluationCaseSchema).default([]),
}).loose().transform(({ items }) => items);

export const PromptEvaluationDatasetFromTracesResponseSchema: z.ZodType<PromptEvaluationDatasetFromTracesResponse> = z.object({
  created_count: z.number(),
}).loose();
