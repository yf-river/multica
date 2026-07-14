export type GitHubPullRequestState = "open" | "closed" | "merged" | "draft";

/** Aggregated CI status for a PR's current head SHA, computed server-side from
 * the latest check_suite per app. `null` when no completed suite has been seen
 * yet (e.g. PR just opened, or repository has no CI configured). */
export type GitHubPullRequestChecksConclusion = "passed" | "failed" | "pending";

/** Raw mirror of GitHub's `mergeable_state`. The UI only surfaces `clean` and
 * `dirty`; the other values (`blocked`, `behind`, `unstable`, `unknown`,
 * `has_hooks`, `draft`) round-trip but render as unknown to avoid asserting
 * "conflicts" for blocking reasons that aren't actual conflicts. */
export type GitHubMergeableState = string;

export interface GitHubInstallation {
  id: string;
  workspace_id: string;
  /** GitHub's numeric installation id — the management handle used by the
   * connect / disconnect flows. Omitted when the caller cannot manage
   * integrations (see `ListGitHubInstallationsResponse.can_manage`). */
  installation_id?: number;
  account_login: string;
  created_at: string;
  /** Display name of the workspace member who connected this installation.
   * Optional because older backends and minimum-visibility deployments may
   * omit it; the UI renders the "connected by" line only when present. */
  connected_by?: string;
}

export interface GitHubPullRequest {
  id: string;
  workspace_id: string;
  repo_owner: string;
  repo_name: string;
  number: number;
  title: string;
  state: GitHubPullRequestState;
  html_url: string;
  branch: string | null;
  author_login: string | null;
  mergeable_state: GitHubMergeableState | null;
  checks_conclusion: GitHubPullRequestChecksConclusion | null;
  checks_passed: number;
  checks_failed: number;
  checks_pending: number;
  /** The server uses 0/0/0 when GitHub has not supplied diff statistics. */
  additions: number;
  deletions: number;
  changed_files: number;
}

export interface ListGitHubInstallationsResponse {
  installations: GitHubInstallation[];
  /** Whether the deployment has GitHub App credentials configured. When false, the Connect button is hidden / disabled. */
  configured: boolean;
  /** Whether the caller can connect / disconnect installations. Non-admin
   * members get `false` along with installations that omit `installation_id`. */
  can_manage: boolean;
}

export interface GitHubConnectResponse {
  /** The GitHub App install URL the browser should open. Empty when `configured` is false. */
  url?: string;
  configured: boolean;
}
