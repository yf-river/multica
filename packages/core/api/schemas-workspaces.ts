import { z } from "zod";
import {
  DEFAULT_WORKSPACE_SETTINGS,
  type MemberWithUser,
  type Workspace,
  type WorkspaceRepo,
  type WorkspaceRepoProbeResponse,
  type WorkspaceSettings,
} from "../types";

const WorkspaceSettingsSchema = z.record(z.string(), z.unknown()).default({}).transform((settings): WorkspaceSettings => {
  const normalized: Record<string, unknown> = { ...settings };
  for (const [key, defaultValue] of Object.entries(DEFAULT_WORKSPACE_SETTINGS)) {
    if (typeof normalized[key] !== "boolean") normalized[key] = defaultValue;
  }
  return normalized as WorkspaceSettings;
});

export const WorkspaceRepoSchema = z.object({
  url: z.string(),
  project_path: z.string().optional(),
  default_branch: z.string().optional(),
  head_commit: z.string().optional(),
  connection_status: z.string().optional(),
  sync_status: z.string().optional(),
  test_status: z.string().optional(),
  last_tested_at: z.string().optional(),
  last_synced_at: z.string().optional(),
}).loose();

export const WorkspaceSchema = z.object({
  id: z.string(),
  name: z.string(),
  slug: z.string(),
  description: z.string().nullable().optional().transform((value) => value ?? null),
  context: z.string().nullable().optional().transform((value) => value ?? null),
  settings: WorkspaceSettingsSchema,
  repos: z.array(WorkspaceRepoSchema).default([]),
  issue_prefix: z.string().default(""),
  avatar_url: z.string().nullable().optional().transform((value) => value ?? null),
}).loose();

export const WorkspaceListSchema = z.array(WorkspaceSchema);

export const WorkspaceRepoProbeResponseSchema = z.object({
  url: z.string(),
  project_path: z.string().default(""),
  default_branch: z.string().default(""),
  branches: z.array(z.string()).default([]),
}).loose();

export const MemberWithUserSchema = z.object({
  id: z.string(),
  user_id: z.string(),
  role: z.string(),
  name: z.string().default(""),
  account: z.string().default(""),
  avatar_url: z.string().nullable().optional().transform((value) => value ?? null),
}).loose();

export const MemberWithUserListSchema = z.array(MemberWithUserSchema);

export const EMPTY_WORKSPACE: Workspace = {
  id: "",
  name: "",
  slug: "",
  description: null,
  context: null,
  settings: { ...DEFAULT_WORKSPACE_SETTINGS },
  repos: [],
  issue_prefix: "",
  avatar_url: null,
};

export const EMPTY_WORKSPACE_REPO: WorkspaceRepo = { url: "" };

export const EMPTY_WORKSPACE_REPO_PROBE_RESPONSE: WorkspaceRepoProbeResponse = {
  url: "",
  project_path: "",
  default_branch: "",
  branches: [],
};

export const EMPTY_MEMBER_WITH_USER: MemberWithUser = {
  id: "",
  user_id: "",
  role: "member",
  name: "",
  account: "",
  avatar_url: null,
};
