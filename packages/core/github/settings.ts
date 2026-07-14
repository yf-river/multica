import { DEFAULT_WORKSPACE_SETTINGS, type Workspace } from "../types";

export interface GitHubSettings {
  /** Master switch. When false, every UI affordance and side-effect is gated off. */
  enabled: boolean;
  /** Issue-detail PR sidebar visibility. Implies `enabled`. */
  prSidebar: boolean;
  /** Co-authored-by trailer in agent commits. Implies `enabled`. */
  coAuthor: boolean;
}

/**
 * Pure derivation from the current workspace settings contract. API response
 * drift is normalized once by WorkspaceSchema before it reaches this layer.
 */
export function deriveGitHubSettings(
  workspace: Pick<Workspace, "settings"> | null | undefined,
): GitHubSettings {
  const s = workspace?.settings ?? DEFAULT_WORKSPACE_SETTINGS;
  const enabled = s.github_enabled;
  return {
    enabled,
    prSidebar: enabled && s.github_pr_sidebar_enabled,
    coAuthor: enabled && s.co_authored_by_enabled,
  };
}
