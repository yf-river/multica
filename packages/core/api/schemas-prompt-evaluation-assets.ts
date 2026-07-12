import { z } from "zod";
import { NonEmptyStringSchema } from "./schemas-internal";

// Runtime response contracts for prompt evaluation assets.
const PromptEvaluationPayloadCaseSchema = z.object({
  case_name: z.string().min(1),
  variables: z.record(z.string(), z.unknown()).default({}),
  expected_contains: z.array(z.string()).default([]),
  tags: z.array(z.string()).default([]),
}).loose();

const PromptEvaluationStrictPayloadSchema = z.object({
  schema_version: z.literal(1),
  schema: z.literal("multica.training_evaluation.payload.v1"),
  语义版本: z.literal("multica.training_evaluation.v1").optional(),
  cases: z.array(PromptEvaluationPayloadCaseSchema).default([]),
  payload_contract: z.record(z.string(), z.unknown()).optional(),
  metric_contract: z.array(z.string()).optional(),
  metric_notes: z.array(z.string()).optional(),
  experiment_dimensions: z.array(z.union([
    z.string(),
    z.object({ name: z.string().min(1) }).loose(),
  ])).optional(),
  experiment_target: z.string().optional(),
  baseline_output: z.string().optional(),
  agent_id: z.string().min(1).optional(),
}).loose();

const PromptEvaluationPayloadSchema = z.record(z.string(), z.unknown()).default({}).superRefine((payload, ctx) => {
  if (payload.schema !== "multica.training_evaluation.payload.v1") return;
  const parsed = PromptEvaluationStrictPayloadSchema.safeParse(payload);
  if (!parsed.success) {
    ctx.addIssue({
      code: "custom",
      message: `invalid training evaluation payload: ${parsed.error.issues.map((issue) => issue.message).join("; ")}`,
    });
  }
});

export const PromptEvaluationAssetSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  prompt_id: z.string().nullable().optional().transform((v) => v ?? null),
  name: z.string(),
  description: z.string().default(""),
  asset_type: z.enum(["数据集", "测试套件"]),
  payload: PromptEvaluationPayloadSchema,
  status: z.enum(["启用", "归档"]).default("启用"),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string(),
  updated_at: z.string(),
  structure_schema: z.string().default("multica.training_evaluation.asset_profile.v1"),
  structured_case_count: z.number().default(0),
  structured_variable_count: z.number().default(0),
  structured_assertion_count: z.number().default(0),
  linked_dataset_count: z.number().default(0),
  linked_prompt_count: z.number().default(0),
  evaluation_dimension_count: z.number().default(0),
  dataset_row_count: z.number().default(0),
  test_suite_case_count: z.number().default(0),
  experiment_dimension_count: z.number().default(0),
}).loose();

export const PromptEvaluationAssetListResponseSchema = z.object({
  items: z.array(PromptEvaluationAssetSchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptEvaluationDatasetVersionSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  dataset_asset_id: NonEmptyStringSchema,
  version: z.number().default(0),
  version_label: z.string().default(""),
  row_count: z.number().default(0),
  row_fingerprint: z.string().default(""),
  metadata: z.record(z.string(), z.unknown()).default({}),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
}).loose();

export const PromptEvaluationDatasetVersionListResponseSchema = z.object({
  items: z.array(PromptEvaluationDatasetVersionSchema).default([]),
  total: z.number().default(0),
}).loose();
