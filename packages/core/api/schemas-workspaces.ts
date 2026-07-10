import { z } from "zod";
import type { MemberWithUser, Workspace, WorkspaceRepo, WorkspaceRepoProbeResponse } from "../types";

export const WorkspaceRepoSchema = z.object({
  url: z.string(),
  description: z.string().optional(),
  provider: z.string().optional(),
  project_path: z.string().optional(),
  default_branch: z.string().optional(),
  head_commit: z.string().optional(),
  commit_sha: z.string().optional(),
  connection_status: z.string().optional(),
  sync_status: z.string().optional(),
  test_status: z.string().optional(),
  last_tested_at: z.string().optional(),
  last_synced_at: z.string().optional(),
  resolve_status: z.string().optional(),
  resolve_error: z.string().optional(),
  last_resolved_at: z.string().optional(),
}).loose();

export const WorkspaceSchema = z.object({
  id: z.string(),
  name: z.string(),
  slug: z.string(),
  description: z.string().nullable().optional().transform((value) => value ?? null),
  context: z.string().nullable().optional().transform((value) => value ?? null),
  settings: z.record(z.string(), z.unknown()).default({}),
  repos: z.array(WorkspaceRepoSchema).default([]),
  issue_prefix: z.string().default(""),
  avatar_url: z.string().nullable().optional().transform((value) => value ?? null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const WorkspaceListSchema = z.array(WorkspaceSchema);

export const WorkspaceRepoProbeResponseSchema = z.object({
  url: z.string(),
  provider: z.string().default(""),
  project_path: z.string().default(""),
  default_branch: z.string().default(""),
  branches: z.array(z.string()).default([]),
  connection_status: z.string().default(""),
  test_status: z.string().default(""),
}).loose();

export const MemberWithUserSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  user_id: z.string(),
  role: z.string(),
  created_at: z.string().default(""),
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
  settings: {},
  repos: [],
  issue_prefix: "",
  avatar_url: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_WORKSPACE_REPO: WorkspaceRepo = { url: "" };

export const EMPTY_WORKSPACE_REPO_PROBE_RESPONSE: WorkspaceRepoProbeResponse = {
  url: "",
  provider: "",
  project_path: "",
  default_branch: "",
  branches: [],
  connection_status: "",
  test_status: "",
};

export const EMPTY_MEMBER_WITH_USER: MemberWithUser = {
  id: "",
  workspace_id: "",
  user_id: "",
  role: "member",
  created_at: "",
  name: "",
  account: "",
  avatar_url: null,
};
