import { DEFAULT_WORKSPACE_SETTINGS, type Workspace } from "../types";

/**
 * Pure derivation from the current workspace settings contract. API response
 * drift is normalized once by WorkspaceSchema before it reaches this layer.
 */
export function deriveGitHubSettings(
  workspace: Pick<Workspace, "settings"> | null | undefined,
) {
  const s = workspace?.settings ?? DEFAULT_WORKSPACE_SETTINGS;
  const enabled = s.github_enabled;
  return {
    enabled,
    prSidebar: enabled && s.github_pr_sidebar_enabled,
    coAuthor: enabled && s.co_authored_by_enabled,
  };
}
