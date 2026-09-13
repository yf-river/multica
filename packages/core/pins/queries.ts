// 本文件封装「固定项（pin）」的 React Query 查询：某个工作区、某个用户的
// 固定项列表（例如侧边栏固定的视图/项目/issue 等）。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 固定项相关查询的 query key 命名空间（按工作区 + 用户维度分隔）。 */
export const pinKeys = {
  all: (wsId: string, userId: string) => ["pins", wsId, userId] as const,
  list: (wsId: string, userId: string) => [...pinKeys.all(wsId, userId), "list"] as const,
};

/** 查询当前用户在某个工作区里的固定项列表。 */
export function pinListOptions(wsId: string, userId: string) {
  return queryOptions({
    queryKey: pinKeys.list(wsId, userId),
    queryFn: () => api.listPins(),
  });
}
