// 本文件封装「通知偏好」的 React Query 查询：读取用户对某工作区的通知偏好。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 通知偏好查询的 query key 命名空间。 */
export const notificationPreferenceKeys = {
  all: (wsId: string) => ["notification-preferences", wsId] as const,
};

/**
 * 读取通知偏好。
 *
 * `workspaceSlug` 通过 `X-Workspace-Slug` 覆盖头把底层请求限定到指定工作区。
 * query key 仍以 `wsId` 为准（slug 与 id 一一对应），所以缓存条目仍是每个
 * 工作区一份；slug 只保证「冷缓存拉取时读的是目标工作区而不是当前激活的
 * 工作区」。省略它则跟随当前激活工作区（设置页的用法）。
 *
 * @param wsId - 工作区 id
 * @param workspaceSlug - 可选的目标工作区 slug
 * @returns React Query options
 */
export function notificationPreferenceOptions(wsId: string, workspaceSlug?: string) {
  return queryOptions({
    queryKey: notificationPreferenceKeys.all(wsId),
    queryFn: () => api.getNotificationPreferences(workspaceSlug),
  });
}
