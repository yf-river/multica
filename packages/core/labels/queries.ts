// 本文件封装「标签」的 React Query 查询：某资源类型下的标签清单、某个
// issue / agent / skill 资源上已挂的标签列表。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { LabelResourceType } from "../types";

/** 标签相关查询的 query key 命名空间。 */
export const labelKeys = {
  all: (wsId: string) => ["labels", wsId] as const,
  list: (wsId: string, resourceType: LabelResourceType = "issue") =>
    [...labelKeys.all(wsId), "list", resourceType] as const,
  detail: (wsId: string, id: string) =>
    [...labelKeys.all(wsId), "detail", id] as const,
  byIssue: (wsId: string, issueId: string) =>
    [...labelKeys.all(wsId), "issue", issueId] as const,
  byResource: (wsId: string, resourceType: "agent" | "skill", resourceId: string) =>
    [...labelKeys.all(wsId), resourceType, resourceId] as const,
};

/** 查询某资源类型下工作区里可用的全部标签。 */
export function labelListOptions(wsId: string, resourceType: LabelResourceType = "issue") {
  return queryOptions({
    queryKey: labelKeys.list(wsId, resourceType),
    queryFn: () => api.listLabels(resourceType),
    select: (data) => data.labels,
  });
}

/** 查询某个 agent / skill 资源上已挂的标签列表。 */
export function resourceLabelsOptions(
  wsId: string,
  resourceType: "agent" | "skill",
  resourceId: string,
) {
  return queryOptions({
    queryKey: labelKeys.byResource(wsId, resourceType, resourceId),
    queryFn: () => api.listLabelsForResource(resourceType, resourceId),
    select: (data) => data.labels,
    enabled: Boolean(resourceId),
  });
}

/** 查询某个 issue 上已挂的标签列表。 */
export function issueLabelsOptions(wsId: string, issueId: string) {
  return queryOptions({
    queryKey: labelKeys.byIssue(wsId, issueId),
    queryFn: () => api.listLabelsForIssue(issueId),
    select: (data) => data.labels,
    enabled: Boolean(issueId),
  });
}
