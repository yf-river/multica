export type MemberRole = "owner" | "admin" | "member";

export interface WorkspaceRepo {
  url: string;
  project_path?: string;
  default_branch?: string;
  head_commit?: string;
  connection_status?: string;
  sync_status?: string;
  test_status?: string;
  last_tested_at?: string;
  last_synced_at?: string;
}

export interface WorkspaceRepoProbeResponse {
  url: string;
  project_path: string;
  default_branch: string;
  branches: string[];
}

export interface WorkspaceSettings extends Record<string, unknown> {
  github_enabled: boolean;
  github_pr_sidebar_enabled: boolean;
  co_authored_by_enabled: boolean;
  github_auto_link_prs_enabled: boolean;
}

export const DEFAULT_WORKSPACE_SETTINGS: WorkspaceSettings = {
  github_enabled: true,
  github_pr_sidebar_enabled: true,
  co_authored_by_enabled: true,
  github_auto_link_prs_enabled: true,
};

export interface Workspace {
  id: string;
  name: string;
  slug: string;
  description: string | null;
  context: string | null;
  settings: WorkspaceSettings;
  repos: WorkspaceRepo[];
  issue_prefix: string;
  avatar_url: string | null;
}

export interface User {
  id: string;
  name: string;
  account: string;
  avatar_url: string | null;
  /**
   * Free-form self-description (role, stack, preferences). Injected into
   * the agent brief so coding agents have cheap, durable context about
   * who is requesting the work. Server always returns a string —
   * NOT NULL DEFAULT '' at the column level, empty when unset.
   */
  profile_description: string;
  /** Pinned IANA tz; null means "use browser-detected tz at render time". */
  timezone: string | null;
}

export interface MemberWithUser {
  id: string;
  user_id: string;
  role: MemberRole;
  name: string;
  account: string;
  avatar_url: string | null;
}
