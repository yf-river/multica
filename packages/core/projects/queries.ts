// 本文件封装「项目」的 React Query 查询：项目列表与单个项目详情。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 项目相关查询的 query key 命名空间。 */
export const projectKeys = {
  all: (wsId: string) => ["projects", wsId] as const,
  list: (wsId: string) => [...projectKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "detail", id] as const,
};

/** 查询工作区里的项目列表。 */
export function projectListOptions(wsId: string) {
  return queryOptions({
    queryKey: projectKeys.list(wsId),
    queryFn: () => api.listProjects(),
    select: (data) => data.projects,
  });
}

/** 查询单个项目详情。 */
export function projectDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: projectKeys.detail(wsId, id),
    queryFn: () => api.getProject(id),
  });
}
