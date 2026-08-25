export type GitHubPullRequestState = "open" | "closed" | "merged" | "draft";

/** Aggregated CI status for a PR's current head SHA, computed server-side from
 * the latest check_suite per app. `null` when no completed suite has been seen
 * yet (e.g. PR just opened, or repository has no CI configured). */
export type GitHubPullRequestChecksConclusion = "passed" | "failed" | "pending";

interface GitHubInstallation {
  id: string;
  account_login: string;
}

export interface GitHubPullRequest {
  id: string;
  repo_owner: string;
  repo_name: string;
  number: number;
  title: string;
  state: GitHubPullRequestState;
  html_url: string;
  author_login: string | null;
  /** Raw GitHub value. The UI only treats `clean` and `dirty` as known. */
  mergeable_state: string | null;
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
  /** Whether the caller can connect / disconnect installations. */
  can_manage: boolean;
}

export interface GitHubConnectResponse {
  /** The GitHub App install URL the browser should open. Empty when `configured` is false. */
  url?: string;
  configured: boolean;
}
