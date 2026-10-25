// 本文件封装收件箱的 React Query 查询：未读列表、归档列表、跨工作区未读汇总，
// 以及若干对 InboxItem 的按 issue 去重/未读派生逻辑。

import { focusManager, queryOptions, useQuery } from "@tanstack/react-query";
import { api } from "../api";
import type { InboxItem, InboxWorkspaceUnread } from "../types";

/** 收件箱相关查询的 query key 命名空间。 */
export const inboxKeys = {
  all: (wsId: string) => ["inbox", wsId] as const,
  list: (wsId: string) => [...inboxKeys.all(wsId), "list"] as const,
  archived: (wsId: string) => [...inboxKeys.all(wsId), "archived"] as const,
  // 账号级（不按工作区分桶）：一份共享缓存条目，装着用户所属每个工作区的
  // 未读数。
  unreadSummary: () => ["inbox", "unread-summary"] as const,
};

/** 查询当前工作区的收件箱未读条目列表。 */
export function inboxListOptions(wsId: string) {
  return queryOptions({
    queryKey: inboxKeys.list(wsId),
    queryFn: () => api.listInbox(),
  });
}

/**
 * 已归档通知的查询，支撑收件箱的「Archived」子视图。
 *
 * 与主列表各自独立一份缓存，而不是像 chat 那样在同一份扁平缓存里做本地
 * 切分：归档会无限增长，所以要打它自己的带上限接口；并且「哪些条目属于哪个
 * 列表」由服务端而不是客户端决定。
 */
export function archivedInboxListOptions(wsId: string) {
  return queryOptions({
    queryKey: inboxKeys.archived(wsId),
    queryFn: () => api.listArchivedInbox(),
  });
}

/**
 * 跨工作区的收件箱未读汇总。全工作区共享一份缓存——数据是账号级的，切换
 * 工作区不会重新拉取；会变的只是「这个未读是不是属于另一个工作区」的派生视图。
 */
export function inboxUnreadSummaryOptions() {
  return queryOptions({
    queryKey: inboxKeys.unreadSummary(),
    queryFn: () => api.getInboxUnreadSummary(),
    enabled: () => focusManager.isFocused(),
    refetchOnWindowFocus: true,
  });
}

/**
 * 判断「除 `currentWsId` 之外的某个工作区」是否有未读收件箱条目。
 *
 * 驱动工作区切换器上的圆点：当前工作区自己的未读已经由 Inbox 导航计数呈现，
 * 这里把它排除，避免重复信号。
 *
 * @param summary - 跨工作区未读汇总
 * @param currentWsId - 当前工作区 id（可为空）
 * @returns 是否存在其他工作区的未读
 */
export function hasOtherWorkspaceUnread(
  summary: InboxWorkspaceUnread[],
  currentWsId: string | null | undefined,
): boolean {
  return summary.some((s) => s.workspace_id !== currentWsId && s.count > 0);
}

/**
 * 有未读收件箱条目的工作区 id 集合。
 *
 * 让工作区切换器的下拉能标出「待处理消息具体在哪个工作区」（聚合的切换器圆点
 * 只能表达「在某处」）。计数为零的工作区被排除。
 *
 * @param summary - 跨工作区未读汇总
 * @returns 有未读的工作区 id 集合
 */
export function unreadWorkspaceIds(summary: InboxWorkspaceUnread[]): Set<string> {
  return new Set(summary.filter((s) => s.count > 0).map((s) => s.workspace_id));
}

/**
 * 指定工作区的未读收件箱计数，口径与收件箱列表 UI 一致：先排除已归档项，
 * 再按 issue 去重——同一个 issue 的三条未读通知只算一次。
 *
 * @param wsId - 工作区 id（可为空，为空时不请求）
 * @returns 未读数
 */
export function inboxUnreadSummaryEnabled(wsId: string | null | undefined, { whileHidden = false }: { whileHidden?: boolean } = {}) {
  return () => !!wsId && (whileHidden || focusManager.isFocused());
}

export function useInboxUnreadCount(wsId: string | null | undefined, options: { whileHidden?: boolean } = {}): number {
  const { data } = useQuery({
    ...inboxUnreadSummaryOptions(),
    enabled: inboxUnreadSummaryEnabled(wsId, options),
    select: (items: InboxWorkspaceUnread[]) => items.find(item => item.workspace_id === wsId)?.count ?? 0,
  });
  return data ?? 0;
}

/**
 * 按 issue_id 对收件箱条目去重（每个 issue 一条，Linear 风格）。
 *
 * 导出给消费方在 useMemo 里用——不要放进 queryOptions 的 select（那会让每次
 * 缓存更新都产生新的数组引用）。
 */
export function deduplicateInboxItems(items: InboxItem[]): InboxItem[] {
  return groupInboxItemsByIssue(items.filter((i) => !i.archived));
}

/**
 * 归档子视图用的同一套分组。正是这个 `archived` 过滤，让一次乐观「取消归档」
 * 能把该行立刻从归档列表里移除——与 `deduplicateInboxItems` 的过滤让乐观
 * 「归档」的行立即从主列表消失如出一辙。
 */
export function deduplicateArchivedInboxItems(items: InboxItem[]): InboxItem[] {
  return groupInboxItemsByIssue(items.filter((i) => i.archived));
}

/**
 * 把一个 issue 的多条通知合并成一条：取该组里最新的通知，并把最早出现的
 * comment_id（如有）带上来，再整体按创建时间倒序。
 *
 * @param items - 未经过去重的条目
 * @returns 按 issue 合并、按时间倒序后的条目
 */
function groupInboxItemsByIssue(items: InboxItem[]): InboxItem[] {
  const groups = new Map<string, InboxItem[]>();
  for (const item of items) {
    const key = item.issue_id ?? item.id;
    const group = groups.get(key) ?? [];
    group.push(item);
    groups.set(key, group);
  }
  const merged: InboxItem[] = [];
  for (const group of groups.values()) {
    group.sort(
      (a, b) =>
        new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
    );
    const newest = group[0];
    if (!newest) continue;

    const commentId =
      newest.details?.comment_id ??
      group.find((item) => item.details?.comment_id)?.details?.comment_id;

    if (commentId && newest.details?.comment_id !== commentId) {
      merged.push({
        ...newest,
        details: { ...(newest.details ?? {}), comment_id: commentId },
      });
      continue;
    }

    merged.push(newest);
  }
  return merged.sort(
    (a, b) =>
      new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
  );
}
