import { z } from "zod";
import type {
  AgentRuntime,
  RuntimeLocalSkillImportRequest,
  RuntimeLocalSkillListRequest,
  RuntimeModelListRequest,
  RuntimeProfile,
} from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

export const RuntimeDeviceSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  daemon_id: z.string().nullable().optional().transform((value) => value ?? null),
  name: z.string(),
  runtime_mode: z.string().default("local"),
  provider: z.string().default(""),
  launch_header: z.string().default(""),
  status: z.string().default("offline"),
  device_info: z.string().default(""),
  metadata: z.record(z.string(), z.unknown()).default({}),
  owner_id: z.string().nullable().optional().transform((value) => value ?? null),
  scope: z.string().default("workspace"),
  profile_id: z.string().nullable().optional().transform((value) => value ?? null),
  last_seen_at: z.string().nullable().optional().transform((value) => value ?? null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const RuntimeDeviceListSchema = z.array(RuntimeDeviceSchema);

export const EMPTY_RUNTIME_DEVICE: AgentRuntime = {
  id: "",
  workspace_id: "",
  daemon_id: null,
  name: "",
  runtime_mode: "local",
  provider: "",
  launch_header: "",
  status: "offline",
  device_info: "",
  metadata: {},
  owner_id: null,
  scope: "workspace",
  profile_id: null,
  last_seen_at: null,
  created_at: "",
  updated_at: "",
};

export const RuntimeCascadeDeleteResponseSchema = z.object({
  status: z.literal("ok"),
  agents_archived: z.number().int().nonnegative(),
  tasks_cancelled: z.number().int().nonnegative(),
});

export const EMPTY_RUNTIME_CASCADE_DELETE_RESPONSE = {
  status: "ok" as const,
  agents_archived: 0,
  tasks_cancelled: 0,
};

const RuntimeModelThinkingLevelSchema = z.object({
  value: NonEmptyStringSchema,
  label: z.string(),
  description: z.string().optional(),
}).loose();

const RuntimeModelThinkingSchema = z.object({
  supported_levels: z.array(RuntimeModelThinkingLevelSchema),
  default_level: z.string().optional(),
}).loose();

const RuntimeModelSchema = z.object({
  id: NonEmptyStringSchema,
  label: z.string(),
  provider: z.string().optional(),
  default: z.boolean().optional(),
  thinking: RuntimeModelThinkingSchema.optional(),
}).loose();

const RuntimeAsyncRequestStatusSchema = z.enum([
  "pending",
  "running",
  "completed",
  "conflict",
  "failed",
  "timeout",
]);

export const RuntimeModelListRequestSchema = z.object({
  id: NonEmptyStringSchema,
  runtime_id: NonEmptyStringSchema,
  status: RuntimeAsyncRequestStatusSchema.exclude(["conflict"]),
  models: z.array(RuntimeModelSchema).optional(),
  supported: z.boolean(),
  error: z.string().optional(),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

const RuntimeLocalSkillSummarySchema = z.object({
  key: NonEmptyStringSchema,
  name: NonEmptyStringSchema,
  description: z.string().optional(),
  source_path: NonEmptyStringSchema,
  provider: NonEmptyStringSchema,
  root: z.enum(["provider", "universal"]).optional(),
  file_count: z.number().int().nonnegative(),
}).loose();

export const RuntimeLocalSkillListRequestSchema = z.object({
  id: NonEmptyStringSchema,
  runtime_id: NonEmptyStringSchema,
  status: RuntimeAsyncRequestStatusSchema,
  skills: z.array(RuntimeLocalSkillSummarySchema).optional(),
  supported: z.boolean(),
  error: z.string().optional(),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

const RuntimeImportedSkillFileSchema = z.object({
  id: NonEmptyStringSchema,
  skill_id: NonEmptyStringSchema,
  path: z.string(),
  content: z.string(),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

const RuntimeImportedSkillSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  name: NonEmptyStringSchema,
  description: z.string().default(""),
  content: z.string(),
  config: z.record(z.string(), z.unknown()).default({}),
  created_by: z.string().nullable().optional().transform((value) => value ?? null),
  created_at: z.string(),
  updated_at: z.string(),
  files: z.array(RuntimeImportedSkillFileSchema).default([]),
}).loose();

const RuntimeLocalSkillConflictSchema = z.object({
  existing_skill_id: NonEmptyStringSchema,
  existing_created_by: z.string().optional(),
  can_overwrite: z.boolean(),
}).loose();

export const RuntimeLocalSkillImportRequestSchema = z.object({
  id: NonEmptyStringSchema,
  runtime_id: NonEmptyStringSchema,
  skill_key: NonEmptyStringSchema,
  name: z.string().optional(),
  description: z.string().optional(),
  action: z.literal("overwrite").optional(),
  target_skill_id: z.string().optional(),
  supports_conflict: z.boolean().optional(),
  status: RuntimeAsyncRequestStatusSchema,
  skill: RuntimeImportedSkillSchema.optional(),
  conflict: RuntimeLocalSkillConflictSchema.optional(),
  error: z.string().optional(),
  created_at: z.string(),
  updated_at: z.string(),
}).superRefine((request, context) => {
  if (request.status === "completed" && !request.skill) {
    context.addIssue({
      code: "custom",
      path: ["skill"],
      message: "completed import requires a skill",
    });
  }
  if (request.status === "conflict" && !request.conflict) {
    context.addIssue({
      code: "custom",
      path: ["conflict"],
      message: "conflict import requires conflict details",
    });
  }
});

export const EMPTY_RUNTIME_MODEL_LIST_REQUEST: RuntimeModelListRequest = {
  id: "",
  runtime_id: "",
  status: "failed",
  supported: false,
  error: "invalid runtime model response",
  created_at: "",
  updated_at: "",
};

export const EMPTY_RUNTIME_LOCAL_SKILL_LIST_REQUEST: RuntimeLocalSkillListRequest = {
  id: "",
  runtime_id: "",
  status: "failed",
  supported: false,
  error: "invalid runtime local skill response",
  created_at: "",
  updated_at: "",
};

export const EMPTY_RUNTIME_LOCAL_SKILL_IMPORT_REQUEST: RuntimeLocalSkillImportRequest = {
  id: "",
  runtime_id: "",
  skill_key: "",
  status: "failed",
  error: "invalid runtime local skill import response",
  created_at: "",
  updated_at: "",
};

export const RuntimeProfileSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  display_name: z.string(),
  // Keep protocol and visibility forward-compatible with newer daemons.
  protocol_family: z.string().default("claude"),
  command_name: z.string(),
  description: z.string().nullable().optional().transform((value) => value ?? null),
  fixed_args: z.array(z.string()).default([]),
  visibility: z.string().default("workspace"),
  created_by: z.string().nullable().optional().transform((value) => value ?? null),
  enabled: z.boolean().default(true),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const RuntimeProfileListResponseSchema = z.object({
  runtime_profiles: z.array(RuntimeProfileSchema).default([]),
}).loose();

export const EMPTY_RUNTIME_PROFILE: RuntimeProfile = {
  id: "",
  workspace_id: "",
  display_name: "",
  protocol_family: "claude",
  command_name: "",
  description: null,
  fixed_args: [],
  visibility: "workspace",
  created_by: null,
  enabled: false,
  created_at: "",
  updated_at: "",
};

export const EMPTY_RUNTIME_PROFILE_LIST_RESPONSE: { runtime_profiles: RuntimeProfile[] } = {
  runtime_profiles: [],
};
