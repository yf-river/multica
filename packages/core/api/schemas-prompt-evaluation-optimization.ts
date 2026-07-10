import { z } from "zod";
import { PromptEvaluationAssetSchema } from "./schemas-prompt-evaluation-assets";
import { PromptEvaluationRunSchema } from "./schemas-prompt-evaluation-runs";
import { PromptLibraryItemSchema } from "./schemas-prompt-library";

// Runtime response contracts for prompt evaluation optimization.
export const PromptEvaluationSkillPatchSchema = z.object({
  schema_version: z.string().default("multica.skill.patch.v1"),
  patch: z.string().default(""),
  patch_hash: z.string().default(""),
  patch_bytes: z.number().default(0),
  candidate_intent: z.enum(["update_existing_skill", "create_operation_skill"]).or(z.string()).default("update_existing_skill"),
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

export const PromptEvaluationOptimizationCandidateSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  asset_id: z.string(),
  run_id: z.string(),
  prompt_id: z.string(),
  candidate_name: z.string(),
  candidate_content: z.string(),
  rationale: z.string().default(""),
  failed_case_count: z.number().default(0),
  source_failure_summary: z.record(z.string(), z.unknown()).default({}),
  source_prompt_snapshot: z.record(z.string(), z.unknown()).default({}),
  metrics: z.record(z.string(), z.unknown()).default({}),
  skill_patch: PromptEvaluationSkillPatchSchema.nullable().optional().transform((v) => v ?? null),
  status: z.enum(["待确认", "已发布", "已拒绝"]).default("待确认"),
  published_prompt_id: z.string().nullable().optional().transform((v) => v ?? null),
  published_at: z.string().default(""),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const PromptEvaluationOptimizationCandidateListResponseSchema = z.object({
  items: z.array(PromptEvaluationOptimizationCandidateSchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptEvaluationSkillInventoryItemSchema = z.object({
  skill_path: z.string().default(""),
  skill_name: z.string().default(""),
  skill_hash: z.string().default(""),
  last_commit: z.string().optional(),
  last_commit_subject: z.string().optional(),
  last_updated_at: z.string().optional(),
  changelog_path: z.string().optional(),
  has_changelog: z.boolean().default(false),
  tracked: z.boolean().default(false),
}).loose();

export const PromptEvaluationSkillInventoryResultSchema = z.object({
  schema_version: z.string().default("multica.skill.inventory.v1"),
  provider: z.string().default("gongfeng"),
  repo: z.string().default(""),
  repo_path: z.string().optional(),
  branch: z.string().default(""),
  head_commit: z.string().default(""),
  skill_root: z.string().default(".codebuddy/skills"),
  items: z.array(PromptEvaluationSkillInventoryItemSchema).default([]),
  discovered_count: z.number().default(0),
  snapshot_time: z.string().default(""),
  source_resource_id: z.string().optional(),
}).loose();

export const PromptEvaluationSkillInventoryResponseSchema = z.object({
  asset: PromptEvaluationAssetSchema,
  inventory: PromptEvaluationSkillInventoryResultSchema,
}).loose();

export const PromptEvaluationSkillSnapshotSchema = z.object({
  schema_version: z.string().default("multica.skill.snapshot.v1"),
  provider: z.string().default("gongfeng"),
  repo: z.string().default(""),
  repo_path: z.string().optional(),
  branch: z.string().default(""),
  base_commit: z.string().default(""),
  skill_path: z.string().default(""),
  skill_hash: z.string().default(""),
  snapshot_time: z.string().default(""),
  source_resource_id: z.string().optional(),
}).loose();

export const PromptEvaluationSkillSnapshotResultSchema = z.object({
  asset: PromptEvaluationAssetSchema,
  snapshot: PromptEvaluationSkillSnapshotSchema,
}).loose();

export const PromptEvaluationSkillCaseDraftSchema = z.object({
  schema_version: z.string().default("multica.skill.case_draft.v1"),
  status: z.string().default("draft"),
  input: z.string().default(""),
  expected_behavior: z.string().default(""),
  verification: z.string().default(""),
  evidence_source: z.string().default(""),
  applicable_skill_hash: z.string().optional(),
  applicable_scope: z.string().default(""),
  source_commit: z.string().default(""),
  commit_subject: z.string().default(""),
  skill_path: z.string().default(""),
  before_hash: z.string().optional(),
  after_hash: z.string().optional(),
}).loose();

export const PromptEvaluationSkillCaseDraftsResultSchema = z.object({
  asset: PromptEvaluationAssetSchema,
  drafts: z.array(PromptEvaluationSkillCaseDraftSchema).default([]),
  created_count: z.number().default(0),
}).loose();

export const PromptEvaluationSkillFreshnessResultSchema = z.object({
  schema_version: z.string().default("multica.skill.freshness.v1"),
  status: z.enum(["fresh", "branch_changed_skill_unchanged", "stale", "conflict", "rebaseable"]).default("stale"),
  reason: z.string().default(""),
  target_branch: z.string().default(""),
  head_commit: z.string().default(""),
  base_commit: z.string().default(""),
  skill_path: z.string().default(""),
  base_skill_hash: z.string().default(""),
  current_skill_hash: z.string().default(""),
  patch_check: z.string().default("not_needed"),
  checked_at: z.string().default(""),
  snapshot: PromptEvaluationSkillSnapshotSchema,
}).loose();

export const PromptEvaluationSkillApplyResultSchema = z.object({
  schema_version: z.string().default("multica.skill.apply.v1"),
  status: z.enum(["dry_run", "applied", "blocked", "conflict"]).default("blocked"),
  reason: z.string().default(""),
  repo_path: z.string().default(""),
  target_branch: z.string().default(""),
  head_commit: z.string().default(""),
  skill_path: z.string().default(""),
  skill_hash_before: z.string().default(""),
  skill_hash_after: z.string().default(""),
  changelog_path: z.string().optional(),
  patch_check: z.string().default("not_run"),
  freshness: PromptEvaluationSkillFreshnessResultSchema,
  changed_files: z.array(z.string()).default([]),
  re_eval_required: z.boolean().default(true),
  re_eval_plan: z.record(z.string(), z.unknown()).default({}),
  checked_at: z.string().default(""),
  applied_at: z.string().optional(),
  snapshot: PromptEvaluationSkillSnapshotSchema,
}).loose();

export const PromptEvaluationSkillApplyCandidateResponseSchema = z.object({
  candidate: PromptEvaluationOptimizationCandidateSchema,
  apply: PromptEvaluationSkillApplyResultSchema,
}).loose();

export const PromptEvaluationSkillReEvalCaseSchema = z.object({
  name: z.string().default(""),
  variables: z.record(z.string(), z.unknown()).default({}),
  expected_contains: z.array(z.string()).default([]),
  tags: z.array(z.string()).default([]),
  input: z.record(z.string(), z.unknown()).default({}),
  expected: z.record(z.string(), z.unknown()).default({}),
  source_commit: z.string().default(""),
  evidence_source: z.string().default(""),
  status: z.string().default("approved"),
}).loose();

export const PromptEvaluationSkillReEvalAssetResponseSchema = z.object({
  candidate: PromptEvaluationOptimizationCandidateSchema,
  asset: PromptEvaluationAssetSchema,
  source_snapshot: PromptEvaluationSkillSnapshotSchema,
  re_eval_snapshot: PromptEvaluationSkillSnapshotSchema,
  case_count: z.number().default(0),
  cases: z.array(PromptEvaluationSkillReEvalCaseSchema).default([]),
  re_eval_plan: z.record(z.string(), z.unknown()).default({}),
}).loose();

export const PromptEvaluationSkillReEvalRunResponseSchema = z.object({
  candidate: PromptEvaluationOptimizationCandidateSchema,
  asset: PromptEvaluationAssetSchema,
  run: PromptEvaluationRunSchema,
  source_snapshot: PromptEvaluationSkillSnapshotSchema,
  re_eval_snapshot: PromptEvaluationSkillSnapshotSchema,
  case_count: z.number().default(0),
  proof_scope: z.string().default("local_prompt_evaluation_run"),
  re_eval_run: z.record(z.string(), z.unknown()).default({}),
}).loose();

export const PublishPromptEvaluationOptimizationCandidateResponseSchema = z.object({
  candidate: PromptEvaluationOptimizationCandidateSchema,
  prompt: PromptLibraryItemSchema,
}).loose();
