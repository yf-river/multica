import type { StorageAdapter } from "../types/storage";

/**
 * Keys that are namespaced per workspace (stored as `${key}:${slug}`).
 *
 * IMPORTANT: When adding a new workspace-scoped persist store or storage key,
 * add its key here so that workspace deletion and logout properly clean it up.
 * Also ensure the store uses `createWorkspaceAwareStorage` for its persist config.
 */
const WORKSPACE_LOCAL_KEYS = [
  "multica_issue_draft",
  "multica_issues_view",
  "multica_issues_scope",
  "multica_my_issues_view",
  "multica_actor_issues_view",
  "multica_quick_create",
  "multica_comment_collapse",
  "multica_comment_drafts",
  "multica_feedback_draft",
  "multica_project_draft",
  "multica_projects_view",
  "multica_agents_view",
  "multica_skills_view",
  "multica_squads_view",
  "multica_autopilots_view",
  "project_issues_view",
  "multica:chat:selectedAgentId",
  "multica:chat:activeSessionId",
  "multica:chat:drafts",
  "multica:chat:draft-attachments",
  "multica:chat:expanded",
  "multica_navigation",
] as const;

const ACCOUNT_LOCAL_KEYS = [
  "multica_recent_issues",
  "multica_recent_contexts",
  "multica_pending_chat_operations",
  "multica_tabs",
] as const;

const ACCOUNT_LOCAL_PREFIXES = [
  ...WORKSPACE_LOCAL_KEYS.map((key) => `${key}:`),
  "multica:training:selected-prompt:",
  "multica:mention-recency:",
] as const;

const ACCOUNT_SESSION_PREFIXES = [
  "multica:training:case-drafts:",
  "multica_cmdF_warned:",
] as const;

export interface WorkspaceStorageScope {
  slug: string;
  id: string;
}

export interface AccountStorageAdapters {
  local: StorageAdapter;
  session: StorageAdapter;
}

/** Remove all workspace-scoped storage entries for the given workspace slug. */
export function clearWorkspaceStorage(
  adapters: AccountStorageAdapters,
  workspace: WorkspaceStorageScope,
): void {
  for (const key of WORKSPACE_LOCAL_KEYS) {
    adapters.local.removeItem(`${key}:${workspace.slug}`);
  }
  adapters.local.removeItem(
    `multica:training:selected-prompt:${workspace.id}`,
  );
  adapters.local.removeItem(`multica:mention-recency:${workspace.id}`);
  adapters.session.removeItem(
    `multica:training:case-drafts:${workspace.id}`,
  );
}

/** Remove every persisted value that can expose one signed-in account to another. */
export function clearAccountStorage(
  adapters: AccountStorageAdapters,
): void {
  for (const key of ACCOUNT_LOCAL_KEYS) adapters.local.removeItem(key);
  for (const key of WORKSPACE_LOCAL_KEYS) adapters.local.removeItem(key);
  removeMatching(adapters.local, ACCOUNT_LOCAL_PREFIXES);
  removeMatching(adapters.session, ACCOUNT_SESSION_PREFIXES);
}

function removeMatching(
  adapter: StorageAdapter,
  prefixes: readonly string[],
): void {
  for (const key of adapter.keys?.() ?? []) {
    if (prefixes.some((prefix) => key.startsWith(prefix))) {
      adapter.removeItem(key);
    }
  }
}
