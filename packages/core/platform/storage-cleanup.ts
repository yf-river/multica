// 本文件提供「按工作区清理本地存储」：删除某工作区 slug 命名空间下所有
// 工作区范围的持久化条目。

import type { StorageAdapter } from "../types/storage";
import { clearRegisteredWorkspaceDrafts } from "../drafts/cleanup-registry";
// 确保每个模块级草稿 store 都在清理运行前注册过自己的 key，这样登出/删除时
// 注册表绝不会只被部分填充。
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
