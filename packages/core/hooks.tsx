// 本文件提供访问当前工作区相关信息的顶层 Hook。

"use client";

import { useCurrentWorkspace } from "./paths/hooks";

/**
 * 获取当前工作区的 UUID。如果在工作区路由之外调用会抛错。
 *
 * 实现：基于 `useCurrentWorkspace()` 推导（URL slug + React Query 列表）。
 * 已不再依赖 React Context——`WorkspaceIdProvider` 在 slug-first 重构中被移除。
 * 抛错语义被保留下来，这样依赖非空值的既有调用方无需再写判空保护。
 *
 * @returns 当前工作区的 ID
 */
export function useWorkspaceId(): string {
  const ws = useCurrentWorkspace();
  if (!ws) throw new Error("useWorkspaceId: no workspace selected — ensure component renders inside a workspace route");
  return ws.id;
}
