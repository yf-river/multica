// 本文件提供视图栏（view bar）的偏好读写与「应用到视图栏」的纯逻辑：
// 用户能隐藏、排序视图栏上的标签，这里负责持久化这些偏好并据此合成最终展示。

"use client";

import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { IssueViewPreference } from "../api/schemas";
import type { IssueViewScope } from "./queries";

/** 视图栏偏好：被隐藏的项 id 列表 + 展示顺序。 */
export interface ViewBarPrefs {
  hidden: string[];
  order: string[];
}

/** 空的视图栏偏好（冻结常量）。 */
export const EMPTY_VIEW_BAR_PREFS: ViewBarPrefs = { hidden: [], order: [] };

/** 视图栏偏好查询的 query key 命名空间。 */
export const issueViewPrefKeys = {
  all: (wsId: string) => ["issue-view-prefs", wsId] as const,
  scope: (wsId: string, scope: IssueViewScope) =>
    [...issueViewPrefKeys.all(wsId), scope.scope_type, scope.scope_id ?? null] as const,
};

/** 查询某作用域下的视图栏偏好。 */
export function issueViewPreferenceOptions(wsId: string, scope: IssueViewScope) {
  return queryOptions({
    queryKey: issueViewPrefKeys.scope(wsId, scope),
    queryFn: () => api.getIssueViewPreference(scope),
    enabled: !!wsId,
  });
}

/**
 * 整份文档的 upsert，带乐观补丁：显示/隐藏切换与拖拽排序是典型的乐观场景
 * （本地结果可预期、同一屏操作、回滚简单）。
 */
export function useUpdateIssueViewPreference(wsId: string, scope: IssueViewScope) {
  const queryClient = useQueryClient();
  const queryKey = issueViewPrefKeys.scope(wsId, scope);
  return useMutation({
    mutationFn: (prefs: ViewBarPrefs) =>
      api.putIssueViewPreference({ ...scope, prefs }),
    onMutate: async (prefs) => {
      await queryClient.cancelQueries({ queryKey });
      const previous = queryClient.getQueryData<IssueViewPreference>(queryKey);
      const optimistic: IssueViewPreference = {
        scope_type: scope.scope_type,
        scope_id: scope.scope_id ?? null,
        updated_at: previous?.updated_at ?? "",
        prefs: { ...prefs },
      };
      queryClient.setQueryData<IssueViewPreference>(queryKey, optimistic);
      return { previous };
    },
    onError: (_err, _prefs, context) => {
      if (context?.previous) queryClient.setQueryData(queryKey, context.previous);
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey });
    },
  });
}

/**
 * 合成视图栏的最终展示：套用用户设置的顺序、去掉被隐藏的项，同时保证内置
 * 锚点（"builtin:" 首项）始终可见，免得用户把自己锁在某块界面外面。
 *
 * 偏好里出现未知 id（已被删除的视图）直接忽略；不在 `order` 里的项按自然
 * 顺序追加到末尾。
 *
 * @param items - 视图栏的全部项
 * @param prefs - 用户偏好（可为空）
 * @param anchorId - 不可隐藏的锚点项 id
 * @returns 合成结果：可见项、隐藏项集合与排好序的全量项
 */
export function applyViewBarPrefs<T extends { barItemId: string }>(
  items: T[],
  prefs: ViewBarPrefs | undefined,
  anchorId: string,
): { visible: T[]; hiddenSet: Set<string>; ordered: T[] } {
  const order = prefs?.order ?? [];
  const hiddenSet = new Set(prefs?.hidden ?? []);
  hiddenSet.delete(anchorId);

  const byId = new Map(items.map((item) => [item.barItemId, item]));
  const ordered: T[] = [];
  for (const id of order) {
    const item = byId.get(id);
    if (item) {
      ordered.push(item);
      byId.delete(id);
    }
  }
  for (const item of items) {
    if (byId.has(item.barItemId)) ordered.push(item);
  }

  return {
    ordered,
    hiddenSet,
    visible: ordered.filter((item) => !hiddenSet.has(item.barItemId)),
  };
}
