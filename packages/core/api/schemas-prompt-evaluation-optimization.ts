import { z } from "zod";
import { PromptEvaluationRunSchema } from "./schemas-prompt-evaluation-runs";
import { NonEmptyStringSchema } from "./schemas-internal";
import { PromptLibraryItemSchema } from "./schemas-prompt-library";

// Runtime response contracts for prompt evaluation optimization.
const PromptEvaluationSkillPatchSchema = z.object({
  schema_version: z.string().default("multica.skill.patch.v1"),
  patch: z.string().default(""),
  patch_hash: z.string().default(""),
  candidate_intent: z.enum(["update_existing_skill", "create_operation_skill"]).or(z.string()),
  operation_skill_key: z.string().optional(),
  operation_skill_path: z.string().optional(),
  operation_skill_reason: z.string().optional(),
  source_snapshot: z.record(z.string(), z.unknown()).optional(),
  source_resource_id: z.string().optional(),
  repo_path: z.string().optional(),
  target_branch: z.string().optional(),
  skill_path: z.string().optional(),
  changelog_path: z.string().optional(),
  expected_improvement: z.string().optional(),
  risk: z.string().optional(),
  verification_plan: z.string().optional(),
  publication_status: z.string().default("draft"),
  created_at: z.string().optional(),
  updated_at: z.string().optional(),
}).loose();

const PromptEvaluationOptimizationCandidateStatusSchema = z.enum(["待确认", "已发布", "已拒绝"]);

export const PromptEvaluationOptimizationCandidateSchema = z.object({
  id: NonEmptyStringSchema,
  run_id: NonEmptyStringSchema,
  candidate_name: z.string(),
  rationale: z.string().default(""),
  failed_case_count: z.number().default(0),
  source_prompt_snapshot: z.record(z.string(), z.unknown()).default({}),
  metrics: z.record(z.string(), z.unknown()).default({}),
  skill_patch: PromptEvaluationSkillPatchSchema.nullable().optional().transform((v) => v ?? null),
  status: PromptEvaluationOptimizationCandidateStatusSchema.default("待确认"),
});

export const PromptEvaluationOptimizationCandidateListResponseSchema = z.object({
  items: z.array(PromptEvaluationOptimizationCandidateSchema).default([]),
}).loose().transform(({ items }) => items);

export const PromptEvaluationOptimizationCandidateDecisionStatusSchema = z.object({
  id: NonEmptyStringSchema,
  status: PromptEvaluationOptimizationCandidateStatusSchema,
}).loose().transform(({ status }) => status);

export const PromptEvaluationSkillFreshnessResultSchema = z.object({
  status: z.enum(["fresh", "branch_changed_skill_unchanged", "stale", "conflict", "rebaseable"]).default("stale"),
  patch_check: z.string().default("not_needed"),
}).loose();

const PromptEvaluationSkillApplyResultSchema = z.object({
  status: z.enum(["dry_run", "applied", "blocked", "conflict"]).default("blocked"),
}).loose();

export const PromptEvaluationSkillApplyStatusSchema = z.object({
  apply: PromptEvaluationSkillApplyResultSchema,
}).loose().transform(({ apply }) => apply.status);

export const PromptEvaluationSkillReEvalAssetResultSchema = z.object({
  asset: z.object({ id: NonEmptyStringSchema }).loose(),
  case_count: z.number().default(0),
}).loose().transform(({ asset, case_count }) => ({
  assetId: asset.id,
  caseCount: case_count,
}));

export const PromptEvaluationSkillReEvalRunStatusSchema = z.object({
  run: PromptEvaluationRunSchema.pick({ id: true, status: true }),
}).loose().transform(({ run }) => run.status);

export const PublishPromptEvaluationOptimizationCandidateNameSchema = z.object({
  prompt: PromptLibraryItemSchema.pick({ name: true }),
}).loose().transform(({ prompt }) => prompt.name);
