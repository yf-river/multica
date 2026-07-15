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
  daemon_id: z.string().nullable(),
  name: z.string(),
  runtime_mode: z.string(),
  provider: z.string(),
  launch_header: z.string(),
  status: z.string(),
  device_info: z.string(),
  metadata: z.record(z.string(), z.unknown()),
  owner_id: z.string().nullable(),
  scope: z.string(),
  profile_id: z.string().nullable(),
  last_seen_at: z.string().nullable(),
}).loose();

export const RuntimeDeviceListSchema = z.array(RuntimeDeviceSchema);

export const EMPTY_RUNTIME_DEVICE: AgentRuntime = {
  id: "",
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

const RuntimeAsyncRequestSchema = z.object({
  id: NonEmptyStringSchema,
  runtime_id: NonEmptyStringSchema,
  status: RuntimeAsyncRequestStatusSchema,
  error: z.string().optional(),
});

export const RuntimeModelListRequestSchema = RuntimeAsyncRequestSchema.extend({
  status: RuntimeAsyncRequestStatusSchema.exclude(["conflict"]),
  models: z.array(RuntimeModelSchema).optional(),
  supported: z.boolean(),
}).loose();

const RuntimeLocalSkillSummarySchema = z.object({
  key: NonEmptyStringSchema,
  name: NonEmptyStringSchema,
  description: z.string().optional(),
  source_path: NonEmptyStringSchema,
  provider: NonEmptyStringSchema,
  file_count: z.number().int().nonnegative(),
}).loose();

export const RuntimeLocalSkillListRequestSchema = RuntimeAsyncRequestSchema.extend({
  skills: z.array(RuntimeLocalSkillSummarySchema).optional(),
  supported: z.boolean(),
}).loose();

const RuntimeImportedSkillFileSchema = z.object({
  path: z.string(),
  content: z.string(),
}).loose();

const RuntimeImportedSkillSchema = z.object({
  id: NonEmptyStringSchema,
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

export const RuntimeLocalSkillImportRequestSchema = RuntimeAsyncRequestSchema.extend({
  action: z.literal("overwrite").optional(),
  skill: RuntimeImportedSkillSchema.optional(),
  conflict: RuntimeLocalSkillConflictSchema.optional(),
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

const failedRuntimeRequest = (error: string) => ({
  id: "",
  runtime_id: "",
  status: "failed" as const,
  error,
});

export const EMPTY_RUNTIME_MODEL_LIST_REQUEST: RuntimeModelListRequest = {
  ...failedRuntimeRequest("invalid runtime model response"),
  supported: false,
};

export const EMPTY_RUNTIME_LOCAL_SKILL_LIST_REQUEST: RuntimeLocalSkillListRequest = {
  ...failedRuntimeRequest("invalid runtime local skill response"),
  supported: false,
};

export const EMPTY_RUNTIME_LOCAL_SKILL_IMPORT_REQUEST: RuntimeLocalSkillImportRequest =
  failedRuntimeRequest("invalid runtime local skill import response");

export const RuntimeProfileSchema = z.object({
  id: z.string(),
  display_name: z.string(),
  // Keep the protocol family forward-compatible with newer daemons.
  protocol_family: z.string().default("claude"),
  command_name: z.string(),
  description: z.string().nullable().optional().transform((value) => value ?? null),
  fixed_args: z.array(z.string()).default([]),
  enabled: z.boolean().default(true),
  updated_at: z.string().default(""),
}).loose();

export const RuntimeProfileListResponseSchema = z.object({
  runtime_profiles: z.array(RuntimeProfileSchema).default([]),
}).loose().transform(({ runtime_profiles }) => runtime_profiles);

export const EMPTY_RUNTIME_PROFILE: RuntimeProfile = {
  id: "",
  display_name: "",
  protocol_family: "claude",
  command_name: "",
  description: null,
  fixed_args: [],
  enabled: false,
  updated_at: "",
};
