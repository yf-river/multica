// 本文件提供「按工作区清理本地存储」：删除某工作区 slug 命名空间下所有
// 工作区范围的持久化条目。

import type { StorageAdapter } from "../types/storage";
import {
  clearRegisteredWorkspaceDrafts,
  registeredDraftKeys,
} from "../drafts/cleanup-registry";
// Ensure every module-level draft store has registered its key before cleanup
// runs, so the registry is never partially populated at logout/delete time.
import "../drafts/register-all-drafts";

/**
 * 非草稿的工作区范围 key（存为 `${key}:${slug}`）。
 *
 * 草稿 store 已不在这里：它们通过 `registerDraftCleanup` 自注册（见
 * `drafts/cleanup-registry`），由下面的 `clearRegisteredWorkspaceDrafts`
 * 清除。这份清单只剩剩余的非草稿「视图/导航」key。
 *
 * 重要：新增非草稿的工作区范围持久化 store 时，把 key 加在这里；草稿 store
 * 则优先用 `createDraftStore`（自动注册）或直接调 `registerDraftCleanup`。
 */
const WORKSPACE_SCOPED_KEYS = [
  "multica_issue_surface_views",
  "multica_issues_view",
  "multica_issues_scope",
  "multica_my_issues_view",
  "multica:chat:selectedAgentId",
  "multica:chat:selectedProjectId",
  "multica:chat:activeSessionId",
  "multica:chat:expanded",
  "multica_navigation",
];

/**
 * 删除给定工作区 slug 命名空间下所有工作区范围的存储条目。
 *
 * @param adapter - 存储适配器
 * @param slug - 目标工作区 slug
 */
export function clearWorkspaceStorage(
  adapter: StorageAdapter,
  slug: string,
) {
  for (const key of WORKSPACE_SCOPED_KEYS) {
    adapter.removeItem(`${key}:${slug}`);
  }
  // 草稿 store 自注册它们的 key；经注册表清除，这样新增草稿 store 绝不会被
  // 一份过时的清单悄悄漏掉。
  clearRegisteredWorkspaceDrafts(adapter, slug);
}

/**
 * Remove workspace-scoped storage for EVERY workspace on this device, without
 * being told which ones exist.
 *
 * `clearWorkspaceStorage` needs a slug, and the slugs come from the workspace
 * list — which a session rejected at the identity probe never loaded. A cold
 * start with a stale token therefore had no way to clean up after the previous
 * session, leaving `multica_comment_drafts:<slug>` and friends for whoever
 * signed in next. Enumerating the keys removes that dependency entirely, and
 * also catches workspaces the last session had left before it ended.
 *
 * Returns false when the adapter cannot list its keys, so the caller can say
 * what it is falling back to rather than silently doing less.
 */
export function clearAllWorkspaceStorage(adapter: StorageAdapter): boolean {
  const storedKeys = adapter.keys?.();
  if (!storedKeys) return false;

  const drafts = registeredDraftKeys();
  const prefixes = [...WORKSPACE_SCOPED_KEYS, ...drafts.workspaceScoped].map(
    (base) => `${base}:`,
  );
  for (const key of storedKeys) {
    if (prefixes.some((prefix) => key.startsWith(prefix))) {
      adapter.removeItem(key);
    }
  }
  // Globally-namespaced draft keys carry no slug to match on.
  for (const key of drafts.global) adapter.removeItem(key);
  return true;
}
