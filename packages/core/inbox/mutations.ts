// 本文件封装收件箱的各类「变更」操作（标记已读/未读、归档/取消归档、全部已读、
// 批量归档等），每个变更都带乐观更新与失败回滚。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { inboxKeys } from "./queries";
import { useWorkspaceId } from "../hooks";
import type { InboxItem } from "../types";

/** 把某条收件箱通知标记为已读，乐观更新主列表与归档列表缓存。 */
export function useMarkInboxRead() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.markInboxRead(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: inboxKeys.all(wsId) });
      const prev = qc.getQueryData<InboxItem[]>(inboxKeys.list(wsId));
      const prevArchived = qc.getQueryData<InboxItem[]>(inboxKeys.archived(wsId));
      const markRead = (old: InboxItem[] | undefined) =>
        old?.map((item) => (item.id === id ? { ...item, read: true } : item));
      qc.setQueryData<InboxItem[]>(inboxKeys.list(wsId), markRead);
      // 从归档子视图打开通知也会把它标记已读——这份缓存也要同步打补丁，
      // 否则它的未读圆点会一直挂到下次重新拉取。
      qc.setQueryData<InboxItem[]>(inboxKeys.archived(wsId), markRead);
      return { prev, prevArchived };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) qc.setQueryData(inboxKeys.list(wsId), ctx.prev);
      if (ctx?.prevArchived) qc.setQueryData(inboxKeys.archived(wsId), ctx.prevArchived);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
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
      qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
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
 * 未读角标由主列表的缓存派生（`useInboxUnreadCount` 在客户端去重），所以这份
 * 乐观补丁无需等网络往返就能抬高角标；`onSettled` 里会重新拉取跨工作区汇总，
 * 它由服务端计算，无法在这里本地修补。
 */
export function useMarkInboxUnread() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.markInboxUnread(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: inboxKeys.all(wsId) });
      const prev = qc.getQueryData<InboxItem[]>(inboxKeys.list(wsId));
      const prevArchived = qc.getQueryData<InboxItem[]>(inboxKeys.archived(wsId));
      const markUnread = (old: InboxItem[] | undefined) =>
        old?.map((item) => (item.id === id ? { ...item, read: false } : item));
      qc.setQueryData<InboxItem[]>(inboxKeys.list(wsId), markUnread);
      qc.setQueryData<InboxItem[]>(inboxKeys.archived(wsId), markUnread);
      return { prev, prevArchived };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) qc.setQueryData(inboxKeys.list(wsId), ctx.prev);
      if (ctx?.prevArchived) qc.setQueryData(inboxKeys.archived(wsId), ctx.prevArchived);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
      // 工作区重新有未读时，切换器圆点必须再次亮起——那个计数在服务端。
      qc.invalidateQueries({ queryKey: inboxKeys.unreadSummary() });
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
      // 两份列表都要：该条刚从主收件箱移进归档。
      qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
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
      const prev = qc.getQueryData<InboxItem[]>(inboxKeys.archived(wsId));
      // 恢复同一个 issue 的全部兄弟条目——服务端取消归档的是整个 issue 组，
      // 乐观补丁也必须同样处理，否则组里其余条目会一直赖在归档列表直到
      // 重新拉取落地。
      const target = prev?.find((i) => i.id === id);
      const issueId = target?.issue_id;
      qc.setQueryData<InboxItem[]>(inboxKeys.archived(wsId), (old) =>
        old?.map((item) =>
          item.id === id || (issueId && item.issue_id === issueId)
            ? { ...item, archived: false }
            : item,
        ),
      );
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) qc.setQueryData(inboxKeys.archived(wsId), ctx.prev);
    },
    onSettled: () => {
      // 两份列表都要：条目在两者之间迁移；以未读状态归档时角标也要重新亮起。
      qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
      qc.invalidateQueries({ queryKey: inboxKeys.unreadSummary() });
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
      qc.invalidateQueries({ queryKey: inboxKeys.list(wsId) });
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
      qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
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
      qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
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
      qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
    },
  });
}
