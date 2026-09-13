// 本文件是收件箱的 WebSocket 事件响应器：收到各类 inbox 事件后，对 React Query
// 缓存做补丁或失效，让界面保持最新。

import type { QueryClient } from "@tanstack/react-query";
import { inboxKeys } from "./queries";
import type { InboxItem, IssuePriority, IssueStatus } from "../types";

/**
 * 收到「收件箱有新条目」事件时刷新列表缓存。
 *
 * 用 invalidateQueries 而不是 setQueryData——触发重新拉取能可靠地通知到所有
 * 观察者。收件箱列表很小，这么做代价低。
 *
 * 两份列表都要刷新：一条指向「已归档 issue」的新通知会把该 issue 重新带回主
 * 收件箱，也就意味着它必须同时离开归档列表。这个归属由服务端裁决
 * （ListArchivedInboxItems 会排除仍有活跃行的 issue），所以两份都重新拉取，
 * 才能保持它们互斥。
 *
 * @param qc - React Query 客户端
 * @param wsId - 事件所属的工作区 id
 */
export function onInboxNew(
  qc: QueryClient,
  wsId: string,
  _item: InboxItem,
) {
  qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
}

/**
 * 给收件箱条目里内嵌的 issue「投影字段」（状态/优先级）打补丁。
 *
 * 两个列表都会同步更新，因为归档行也暴露同样的 issue 字段和过滤控件。
 *
 * @param qc - React Query 客户端
 * @param wsId - 工作区 id
 * @param issueId - 要更新的 issue id
 * @param patch - 要覆盖的字段（状态/优先级可选）
 */
export function patchInboxIssueProjection(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  patch: { status?: IssueStatus; priority?: IssuePriority },
) {
  const project = (old: InboxItem[] | undefined) => {
    if (!old) return old;
    let changed = false;
    const next = old.map((item) => {
      if (item.issue_id !== issueId) return item;
      changed = true;
      return {
        ...item,
        ...(patch.status !== undefined
          ? { issue_status: patch.status }
          : {}),
        // 别在老后端返回的数据上「制造」出投影字段。能力探测依赖 `undefined`
        // 继续表达「这个接口版本不提供 issue_priority」。
        ...(patch.priority !== undefined && item.issue_priority !== undefined
          ? { issue_priority: patch.priority }
          : {}),
      };
    });
    return changed ? next : old;
  };
  qc.setQueryData<InboxItem[]>(inboxKeys.list(wsId), project);
  // 归档行暴露同样的 issue 字段与过滤控件。
  qc.setQueryData<InboxItem[]>(inboxKeys.archived(wsId), project);
}

/** 便捷方法：只给某个 issue 的收件箱投影补丁更新「状态」字段。 */
export function patchInboxIssueStatus(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  status: IssueStatus,
) {
  patchInboxIssueProjection(qc, wsId, issueId, { status });
}

/** WS 事件响应：某 issue 状态变化后，同步收件箱条目里的投影状态。 */
export function onInboxIssueStatusChanged(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  status: IssueStatus,
) {
  patchInboxIssueStatus(qc, wsId, issueId, status);
}

// 与数据库里 inbox_item.issue_id 上的 ON DELETE CASCADE 对应：issue 被删除时，
// 所有引用它的收件箱条目在服务端也没了，所以缓存里也要一并移除——归档列表
// 同样存着这些 issue 的行，也要一起清理。
/** WS 事件响应：某 issue 被删除后，从两个收件箱缓存里丢弃它的所有条目。 */
export function onInboxIssueDeleted(
  qc: QueryClient,
  wsId: string,
  issueId: string,
) {
  const drop = (old: InboxItem[] | undefined) =>
    old?.filter((i) => i.issue_id !== issueId);
  qc.setQueryData<InboxItem[]>(inboxKeys.list(wsId), drop);
  qc.setQueryData<InboxItem[]>(inboxKeys.archived(wsId), drop);
}

// 主列表和归档列表都要刷新。任何收件箱事件都可能让条目跨过这条边界（归档、
// 取消归档、或新通知复活已归档的 issue），而归属由服务端决定，所以两者总是
// 一起失效。
/** WS 通用事件响应：刷新某工作区收件箱的两份列表缓存。 */
export function onInboxInvalidate(qc: QueryClient, wsId: string) {
  qc.invalidateQueries({ queryKey: inboxKeys.all(wsId) });
}

// 刷新跨工作区未读汇总（工作区切换器圆点）。该汇总横跨所有工作区，所以无论
// 事件来自哪个工作区都要失效它——包括来自非当前工作区的已读/归档事件，那些
// 事件是工作区范围的列表失效够不着的。
/** WS 通用事件响应：刷新跨工作区未读汇总缓存。 */
export function onInboxSummaryInvalidate(qc: QueryClient) {
  qc.invalidateQueries({ queryKey: inboxKeys.unreadSummary() });
}
