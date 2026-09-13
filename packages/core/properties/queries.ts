// 本文件封装「自定义属性定义」的 React Query 查询：工作区属性列表。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 属性相关查询的 query key 命名空间。 */
export const propertyKeys = {
  all: (wsId: string) => ["properties", wsId] as const,
  list: (wsId: string, includeArchived = false) =>
    [...propertyKeys.all(wsId), "list", includeArchived] as const,
};

/** 查询工作区的属性定义列表（可按是否包含已归档过滤）。 */
export function propertyListOptions(wsId: string, includeArchived = false) {
  return queryOptions({
    queryKey: propertyKeys.list(wsId, includeArchived),
    queryFn: () => api.listProperties(includeArchived),
    select: (data) => data.properties,
  });
}
