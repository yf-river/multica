// 本文件封装收件箱的各类「变更」操作（标记已读/未读、归档/取消归档、全部已读、
// 批量归档等），每个变更都带乐观更新与失败回滚。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { inboxKeys, mapArchivedInboxCache, patchArchivedInboxCaches, type ArchivedInboxCache } from "./queries";
import { onInboxInvalidate, onInboxSummaryInvalidate } from "./ws-updaters";
import { useWorkspaceId } from "../hooks";
import type { InboxItem } from "../types";

/**
 * Re-read every inbox cache a write can change: both workspace lists and the
 * cross-workspace unread summary.
 *
 * The unread badge reads that summary (`useInboxUnreadCount`), and it lives
 * under its own account-level key which `inboxKeys.all(wsId)` does not reach.
 * Every mutation here can change the number it holds, so each one refreshes it
 * once the server has confirmed, rather than waiting for the WebSocket echo of
 * its own action.
 *
 * Deliberately the same entry points realtime uses, not local copies: both
 * refreshes have to cancel any in-flight request first, and a mutation racing
 * a first load hits exactly the same hole a WS event does. A plain
 * `invalidateQueries` on the list here would let the list fall behind the
 * badge — the two are rendered side by side (see `refreshInboxQuery`).
 *
 * Not awaited by `onSettled`: the mutation is finished once the server has
 * answered, and a background refresh should not hold its lifecycle open.
 *
 * The rows are patched optimistically but the badge is NOT: it follows the
 * server's confirmation, which buys a single writer at the cost of the badge
 * trailing the row. Deriving it locally instead — recomputing the count from
 * the list cache and writing that back — reads as instant but is unsound: a
 * list cache proves only that the list was loaded ONCE, never that it is
 * complete or concurrent with the summary, and the account-level summary
 * request is not cancelled by the workspace-scoped `cancelQueries` below, so a
 * response already in flight lands on top of the local value anyway. Under
 * pagination it would be wrong by construction — one loaded page cannot
 * produce a global count. If instant feedback is wanted later, it has to be a
 * per-group delta that handles the race, not a recomputed total.
 */
function refreshInboxAfterWrite(qc: QueryClient, wsId: string) {
  void onInboxInvalidate(qc, wsId);
  void onInboxSummaryInvalidate(qc);
}

export function useMarkInboxRead() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.markInboxRead(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: inboxKeys.all(wsId) });
      const prev = qc.getQueryData<InboxItem[]>(inboxKeys.list(wsId));
      const markRead = (old: InboxItem[] | undefined) =>
        old?.map((item) => (item.id === id ? { ...item, read: true } : item));
      qc.setQueryData<InboxItem[]>(inboxKeys.list(wsId), markRead);
      // Opening a notification from the archived sub-view marks it read too —
      // patch that cache as well, or its unread dot would sit there until the
      // next refetch.
      const prevArchived = patchArchivedInboxCaches(qc, wsId, (items) => markRead(items) ?? items);
      return { prev, prevArchived };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) qc.setQueryData(inboxKeys.list(wsId), ctx.prev);
      for (const [key, data] of ctx?.prevArchived ?? []) qc.setQueryData(key, data);
    },
    onSettled: () => {
      refreshInboxAfterWrite(qc, wsId);
    },
  });
}

/** 重试某条「来源上下文快捷创建」失败的任务，成功后失效收件箱缓存。 */
export function useRetrySourceContextQuickCreate() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (taskId: string) => api.retrySourceContextQuickCreate(taskId),
    onSettled: () => {
      refreshInboxAfterWrite(qc, wsId);
    },
  });
}

/**
 * 把一条通知翻回「未读」——{@link useMarkInboxRead} 的逆操作。
 *
 * 与「标记已读」同样的乐观结构（结果可预期、无跳转、回滚简单），并且基于
 * 同样理由同时给两个缓存打补丁：一条通知可以从任一列表里操作，若另一个列表
 * 保持陈旧，切视图后会看到同一条通知两个不同的已读状态。
 *
 * 已读状态在列表缓存中即时更新；未读角标由服务端跨工作区汇总计算，写入后重新拉取。
 */
export function useMarkInboxUnread() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.markInboxUnread(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: inboxKeys.all(wsId) });
      const prev = qc.getQueryData<InboxItem[]>(inboxKeys.list(wsId));
      const markUnread = (old: InboxItem[] | undefined) =>
        old?.map((item) => (item.id === id ? { ...item, read: false } : item));
      qc.setQueryData<InboxItem[]>(inboxKeys.list(wsId), markUnread);
      const prevArchived = patchArchivedInboxCaches(qc, wsId, (items) => markUnread(items) ?? items);
      return { prev, prevArchived };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) qc.setQueryData(inboxKeys.list(wsId), ctx.prev);
      for (const [key, data] of ctx?.prevArchived ?? []) qc.setQueryData(key, data);
    },
    onSettled: () => {
      // The switcher dot must light again when the workspace goes back to
      // having unread items — that count lives on the server.
      refreshInboxAfterWrite(qc, wsId);
    },
  });
}

/** 归档一条（或同一 issue 的全部）收件箱通知，乐观从主列表移除。 */
export function useArchiveInbox() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.archiveInbox(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: inboxKeys.list(wsId) });
      const prev = qc.getQueryData<InboxItem[]>(inboxKeys.list(wsId));
      // 归档同一个 issue 关联的所有条目（与 store 行为一致）。
      const target = prev?.find((i) => i.id === id);
      const issueId = target?.issue_id;
      qc.setQueryData<InboxItem[]>(inboxKeys.list(wsId), (old) =>
        old?.map((item) =>
          item.id === id || (issueId && item.issue_id === issueId)
            ? { ...item, archived: true }
            : item,
        ),
      );
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) qc.setQueryData(inboxKeys.list(wsId), ctx.prev);
    },
    onSettled: () => {
      // Both lists: the item just moved from the main inbox into the archive.
      refreshInboxAfterWrite(qc, wsId);
    },
  });
}

/**
 * 把一条已归档通知恢复到主收件箱。
 *
 * 只对归档缓存做乐观更新：在那里翻转 `archived`，会让该行立刻离开归档列表
 * （去重辅助函数就是按它过滤的），用户留在原地，回滚只需恢复一份快照。主列表
 * 交给 `onSettled` 处理——恢复后它的内容由服务端裁决（哪些兄弟行回来、它们
 * 的已读状态、顺序），所以在客户端失效重拉而不是重建。
 */
export function useUnarchiveInbox() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.unarchiveInbox(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: inboxKeys.archived(wsId) });
      // Resolve the group once across every page and deep-link cache.
      let issueId: string | null | undefined;
      for (const [, data] of qc.getQueriesData<ArchivedInboxCache>({ queryKey: inboxKeys.archived(wsId) })) {
        if (data) mapArchivedInboxCache(data, (items) => {
          issueId ??= items.find((item) => item.id === id)?.issue_id;
          return items;
        });
      }
      const prev = patchArchivedInboxCaches(qc, wsId, (items) => items.map((item) =>
        item.id === id || (issueId && item.issue_id === issueId) ? { ...item, archived: false } : item));
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      for (const [key, data] of ctx?.prev ?? []) qc.setQueryData(key, data);
    },
    onSettled: () => {
      // Both lists: the item moves from one to the other, and the unread badge
      // rises again when it was archived unread.
      refreshInboxAfterWrite(qc, wsId);
    },
  });
}

/** 把主列表里所有未归档通知全部标记为已读，乐观更新主列表。 */
export function useMarkAllInboxRead() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: () => api.markAllInboxRead(),
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: inboxKeys.list(wsId) });
      const prev = qc.getQueryData<InboxItem[]>(inboxKeys.list(wsId));
      qc.setQueryData<InboxItem[]>(inboxKeys.list(wsId), (old) =>
        old?.map((item) =>
          !item.archived ? { ...item, read: true } : item,
        ),
      );
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(inboxKeys.list(wsId), ctx.prev);
    },
    onSettled: () => {
      refreshInboxAfterWrite(qc, wsId);
    },
  });
}

// 下面三个批量归档变更都会把条目移进归档，所以 settle 时都要失效两份列表。
/** 归档全部收件箱通知。 */
export function useArchiveAllInbox() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: () => api.archiveAllInbox(),
    onSettled: () => {
      refreshInboxAfterWrite(qc, wsId);
    },
  });
}

/** 归档全部「已读」的收件箱通知。 */
export function useArchiveAllReadInbox() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: () => api.archiveAllReadInbox(),
    onSettled: () => {
      refreshInboxAfterWrite(qc, wsId);
    },
  });
}

/** 归档与已完成 issue 相关的收件箱通知。 */
export function useArchiveCompletedInbox() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: () => api.archiveCompletedInbox(),
    onSettled: () => {
      refreshInboxAfterWrite(qc, wsId);
    },
  });
}
