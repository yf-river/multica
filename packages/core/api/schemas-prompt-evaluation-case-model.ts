import { z } from "zod";
import { NonEmptyStringSchema } from "./schemas-internal";

/** Case primitives shared by asset restore and case endpoint contracts. */
export const PromptEvaluationCaseAssertionSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  asset_id: NonEmptyStringSchema,
  case_id: NonEmptyStringSchema,
  assertion_index: z.number().default(0),
  assertion_type: z.literal("包含文本").default("包含文本"),
  expected_text: z.string().default(""),
  status: z.enum(["启用", "归档", "draft", "approved", "active"]).default("启用"),
  source: z.string().default("expected_contains"),
  created_at: z.string().default(""),
}).loose();

export const PromptEvaluationCaseSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  asset_id: NonEmptyStringSchema,
  prompt_id: z.string().nullable().optional().transform((v) => v ?? null),
  case_index: z.number().default(0),
  case_name: z.string().default(""),
  variables: z.record(z.string(), z.unknown()).default({}),
  expected_contains: z.array(z.unknown()).default([]),
  assertions: z.array(PromptEvaluationCaseAssertionSchema).default([]),
  input: z.record(z.string(), z.unknown()).default({}),
  expected: z.record(z.string(), z.unknown()).default({}),
  tags: z.array(z.unknown()).default([]),
  status: z.enum(["启用", "归档", "draft", "approved", "active"]).default("启用"),
  source: z.string().default("payload"),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();
