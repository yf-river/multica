import { z } from "zod";
import type {
  GitHubConnectResponse,
  GitHubPullRequest,
  ListGitHubInstallationsResponse,
} from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

const ExternalHttpsURLSchema = z.string().url().refine((value) => {
  try {
    const url = new URL(value);
    return url.protocol === "https:";
  } catch {
    return false;
  }
}, "expected an HTTPS URL");

const GitHubInstallURLSchema = ExternalHttpsURLSchema.refine((value) => {
  try {
    return new URL(value).hostname === "github.com";
  } catch {
    return false;
  }
}, "expected an https://github.com URL");

const GitHubInstallationSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  installation_id: z.number().optional(),
  account_login: NonEmptyStringSchema,
  account_type: NonEmptyStringSchema,
  account_avatar_url: z.string().nullable(),
  created_at: z.string().default(""),
  connected_by: z.string().optional(),
});

export const GitHubInstallationListResponseSchema = z.object({
  installations: z.array(GitHubInstallationSchema).default([]),
  configured: z.boolean(),
  can_manage: z.boolean().default(false),
});

export const GitHubConnectResponseSchema = z.object({
  url: z.string().optional(),
  configured: z.boolean(),
}).superRefine((response, context) => {
  if (!response.configured) return;
  const parsed = GitHubInstallURLSchema.safeParse(response.url);
  if (!parsed.success) {
    context.addIssue({
      code: "custom",
      path: ["url"],
      message: "configured GitHub response requires a safe install URL",
    });
  }
}).transform((response) => response.configured
  ? response
  : { configured: false });

const GitHubPullRequestSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  repo_owner: NonEmptyStringSchema,
  repo_name: NonEmptyStringSchema,
  number: z.number(),
  title: z.string(),
  state: NonEmptyStringSchema,
  html_url: ExternalHttpsURLSchema,
  branch: z.string().nullable(),
  author_login: z.string().nullable(),
  author_avatar_url: z.string().nullable(),
  merged_at: z.string().nullable(),
  closed_at: z.string().nullable(),
  pr_created_at: z.string().default(""),
  pr_updated_at: z.string().default(""),
  mergeable_state: z.string().nullable().optional(),
  checks_conclusion: z.string().nullable().optional(),
  checks_passed: z.number().optional(),
  checks_failed: z.number().optional(),
  checks_pending: z.number().optional(),
  additions: z.number().optional(),
  deletions: z.number().optional(),
  changed_files: z.number().optional(),
});

export const GitHubPullRequestListResponseSchema = z.object({
  pull_requests: z.array(GitHubPullRequestSchema).default([]),
});

export const EMPTY_GITHUB_CONNECT_RESPONSE: GitHubConnectResponse = {
  configured: false,
};

export const EMPTY_GITHUB_INSTALLATION_LIST_RESPONSE: ListGitHubInstallationsResponse = {
  installations: [],
  configured: false,
  can_manage: false,
};

export const EMPTY_GITHUB_PULL_REQUEST_LIST_RESPONSE: { pull_requests: GitHubPullRequest[] } = {
  pull_requests: [],
};
