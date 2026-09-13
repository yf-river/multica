// 本文件封装「已保存的 issue 视图」的 React Query 查询与权限判断：
// 某作用域下的视图列表、单个视图详情，以及谁能管理该视图。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 某块界面（按 scope_type + scope_id 划分）所归属的视图容器。 */
export interface IssueViewScope {
  scope_type: "workspace" | "my" | "project";
  scope_id?: string | null;
}

/** 已保存视图相关查询的 query key 命名空间。 */
export const issueViewKeys = {
  all: (wsId: string) => ["issue-views", wsId] as const,
  list: (wsId: string, scope: IssueViewScope) =>
    [...issueViewKeys.all(wsId), scope.scope_type, scope.scope_id ?? null] as const,
  detail: (wsId: string, id: string) =>
    [...issueViewKeys.all(wsId), "detail", id] as const,
};

/** 查询某作用域下的全部已保存视图。 */
export function issueViewListOptions(wsId: string, scope: IssueViewScope) {
  return queryOptions({
    queryKey: issueViewKeys.list(wsId, scope),
    queryFn: () => api.listIssueViews(scope),
    enabled: !!wsId,
  });
}

/** 按 id 查询单个视图——侧边栏的固定行用它解析标签与跳转目标。 */
export function issueViewDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: issueViewKeys.detail(wsId, id),
    queryFn: () => api.getIssueView(id),
    enabled: !!wsId && !!id,
  });
}

/**
 * 服务端 canManageIssueView 的客户端镜像：视图的所有者，或「分享到工作区的
 * 视图」对工作区 owner/admin。决定哪些操作入口渲染出来——服务端在每次写入时
 * 会重新校验。
 *
 * @param view - 视图的归属字段
 * @param userId - 当前用户 id（可为空）
 * @param role - 当前用户在工作区里的角色（可为空）
 * @returns 当前用户能否管理该视图
 */
export function canManageIssueView(
  view: { owner_id: string; visibility: string },
  userId: string | null,
  role: string | null | undefined,
): boolean {
  if (!userId) return false;
  if (view.owner_id === userId) return true;
  return view.visibility === "workspace" && (role === "owner" || role === "admin");
}
