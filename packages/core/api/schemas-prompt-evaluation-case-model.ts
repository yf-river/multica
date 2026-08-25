import { z } from "zod";
import { NonEmptyStringSchema } from "./schemas-internal";

export const PromptEvaluationCaseSchema = z.object({
  id: NonEmptyStringSchema,
  asset_id: NonEmptyStringSchema,
  prompt_id: z.string().nullable().optional().transform((v) => v ?? null),
  case_index: z.number().default(0),
  case_name: z.string().default(""),
  variables: z.record(z.string(), z.unknown()).default({}),
  expected_contains: z.array(z.unknown()).default([]),
  input: z.record(z.string(), z.unknown()).default({}),
  expected: z.record(z.string(), z.unknown()).default({}),
  tags: z.array(z.unknown()).default([]),
  status: z.enum(["启用", "归档", "draft", "approved", "active"]).default("启用"),
  source: z.string().default("payload"),
  updated_at: z.string().default(""),
}).loose();

export const PromptEvaluationCaseMutationResultSchema = PromptEvaluationCaseSchema.pick({ id: true, case_name: true }).strip();
