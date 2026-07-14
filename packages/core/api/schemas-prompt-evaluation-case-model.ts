import { z } from "zod";
import { NonEmptyStringSchema } from "./schemas-internal";

export const PromptEvaluationCaseSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
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
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();
