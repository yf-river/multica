// 本文件封装「快捷动作目录」的 React Query 查询（MUL-5465）。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/**
 * 快捷动作目录查询（MUL-5465）。
 *
 * 只有一份投影，供设置页与 issue 侧边栏共用。过去另有一份按调用权限过滤的
 * 「仅可运行」投影；它被删掉了，因为两个看着同一个 issue 的人会看到两套
 * 侧边栏却没有任何解释。权限现在只在运行那一刻回答一次。
 */
export const quickActionKeys = {
  all: (wsId: string) => ["quick-actions", wsId] as const,
  list: (wsId: string, includeArchived = false) =>
    [...quickActionKeys.all(wsId), "list", includeArchived] as const,
};

/** 查询快捷动作目录（可按是否包含已归档过滤）。 */
export function quickActionListOptions(wsId: string, includeArchived = false) {
  return queryOptions({
    queryKey: quickActionKeys.list(wsId, includeArchived),
    queryFn: () => api.listQuickActions({ includeArchived }),
    select: (data) => data.quick_actions,
  });
}
