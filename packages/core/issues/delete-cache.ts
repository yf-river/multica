// 议题删除后的缓存清理：把被删议题从所有相关列表/子议题/聚合缓存里移除并失效。
import type {
  InfiniteData,
  QueryClient,
  QueryKey,
} from "@tanstack/react-query";
import {
  agentActivityKeys,
  agentRunCountsKeys,
  agentTaskSnapshotKeys,
  agentTasksKeys,
} from "../agents/queries";
import { labelKeys } from "../labels/queries";
import type {
  Issue,
  ListIssuesCache,
  ListIssuesResponse,
} from "../types";
import { findIssueLocation, removeIssueFromBuckets } from "./cache-helpers";
import { issueKeys } from "./queries";
import { useRecentIssuesStore } from "./stores/recent-issues-store";

/** 删除议题时收集到的缓存元数据：所有需要一并清理的父议题 id。 */
export type DeletedIssueCacheMetadata = {
  parentIssueIds: string[];
};

function collectParentId(
  parentIssueIds: Set<string>,
  parentId: string | null | undefined,
) {
  if (parentId) parentIssueIds.add(parentId);
}

function collectParentFromListCache(
  parentIssueIds: Set<string>,
  data: ListIssuesCache | undefined,
  issueId: string,
) {
  const parentId = data
    ? findIssueLocation(data, issueId)?.issue.parent_issue_id
    : undefined;
  collectParentId(parentIssueIds, parentId);
}

function parentIdFromChildrenKey(key: QueryKey) {
  const parentId = key[key.length - 1];
  return typeof parentId === "string" ? parentId : null;
}

/** 扫描各类缓存，收集被删议题涉及的所有父议题 id。 */
export function collectDeletedIssueCacheMetadata(
  qc: QueryClient,
  wsId: string,
  issueId: string,
): DeletedIssueCacheMetadata {
  const parentIssueIds = new Set<string>();

  const detail = qc.getQueryData<Issue>(issueKeys.detail(wsId, issueId));
  collectParentId(parentIssueIds, detail?.parent_issue_id);

  for (const [, data] of qc.getQueriesData<ListIssuesCache>({
    queryKey: issueKeys.list(wsId),
  })) {
    collectParentFromListCache(parentIssueIds, data, issueId);
  }

  for (const [, data] of qc.getQueriesData<
    InfiniteData<ListIssuesResponse, number>
  >({ queryKey: issueKeys.flatAll(wsId) })) {
    for (const page of data?.pages ?? []) {
      collectParentId(
        parentIssueIds,
        page.issues.find((issue) => issue.id === issueId)?.parent_issue_id,
      );
    }
  }

  for (const [, data] of qc.getQueriesData<ListIssuesCache>({
    queryKey: issueKeys.myAll(wsId),
  })) {
    collectParentFromListCache(parentIssueIds, data, issueId);
  }

  for (const [key, data] of qc.getQueriesData<Issue[]>({
    queryKey: [...issueKeys.all(wsId), "children"],
  })) {
    const child = data?.find((issue) => issue.id === issueId);
    if (!child) continue;
    collectParentId(parentIssueIds, child.parent_issue_id);
    collectParentId(parentIssueIds, parentIdFromChildrenKey(key));
  }

  return { parentIssueIds: Array.from(parentIssueIds) };
}

/** 从工作区列表、myList 列表和所有平铺列表缓存里移除被删议题（并修正 total）。 */
export function pruneDeletedIssueFromListCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
) {
  for (const [key] of qc.getQueriesData<ListIssuesCache>({
    queryKey: issueKeys.list(wsId),
  })) {
    qc.setQueryData<ListIssuesCache>(key, (old) =>
      old ? removeIssueFromBuckets(old, issueId) : old,
    );
  }

  for (const [key] of qc.getQueriesData<ListIssuesCache>({
    queryKey: issueKeys.myAll(wsId),
  })) {
    qc.setQueryData<ListIssuesCache>(key, (old) =>
      old ? removeIssueFromBuckets(old, issueId) : old,
    );
  }

  for (const [key, data] of qc.getQueriesData<
    InfiniteData<ListIssuesResponse, number>
  >({ queryKey: issueKeys.flatAll(wsId) })) {
    if (!data?.pages) continue;
    const found = data.pages.some((page) =>
      page.issues.some((issue) => issue.id === issueId),
    );
    if (!found) continue;
    qc.setQueryData<InfiniteData<ListIssuesResponse, number>>(key, {
      ...data,
      pages: data.pages.map((page) => ({
        ...page,
        total: Math.max(0, page.total - 1),
        issues: page.issues.filter((issue) => issue.id !== issueId),
      })),
    });
  }
}

/** 从各父议题的“子议题列表”缓存里移除被删议题。 */
export function pruneDeletedIssueFromParentChildrenCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  metadata: DeletedIssueCacheMetadata,
) {
  for (const parentId of metadata.parentIssueIds) {
    qc.setQueryData<Issue[]>(issueKeys.children(wsId, parentId), (old) =>
      old?.filter((issue) => issue.id !== issueId),
    );
  }
}

/** 失效被删议题各父议题的子议题、子进度等缓存。 */
export function invalidateDeletedIssueParentCaches(
  qc: QueryClient,
  wsId: string,
  metadata: DeletedIssueCacheMetadata,
) {
  if (metadata.parentIssueIds.length === 0) return;
  for (const parentId of metadata.parentIssueIds) {
    qc.invalidateQueries({ queryKey: issueKeys.children(wsId, parentId) });
  }
  qc.invalidateQueries({ queryKey: issueKeys.childProgress(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.childrenByParentsAll(wsId) });
}

/** 失效依赖议题存在性的 Agent 活动/运行计数/任务等缓存。 */
export function invalidateDeletedIssueDependentCaches(
  qc: QueryClient,
  wsId: string,
) {
  qc.invalidateQueries({ queryKey: agentTaskSnapshotKeys.list(wsId) });
  qc.invalidateQueries({ queryKey: agentActivityKeys.last30d(wsId) });
  qc.invalidateQueries({ queryKey: agentRunCountsKeys.last30d(wsId) });
  qc.invalidateQueries({ queryKey: agentTasksKeys.all(wsId) });
}

/** 失效该议题作用域下的时间线、反应、订阅者、用量、附件、任务、子议题、标签等缓存。 */
export function invalidateIssueScopedCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
) {
  qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.reactions(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.subscribers(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.usage(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.attachments(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.tasks(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.children(wsId, issueId) });
  qc.invalidateQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });
}

/** 议题删除后的完整缓存清理入口：修剪列表/子议题、移除作用域缓存、失效聚合缓存。 */
export function cleanupDeletedIssueCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  metadata = collectDeletedIssueCacheMetadata(qc, wsId, issueId),
) {
  pruneDeletedIssueFromListCaches(qc, wsId, issueId);
  pruneDeletedIssueFromParentChildrenCaches(qc, wsId, issueId, metadata);
  invalidateDeletedIssueParentCaches(qc, wsId, metadata);

  qc.removeQueries({ queryKey: issueKeys.detail(wsId, issueId) });
  qc.removeQueries({ queryKey: issueKeys.timeline(issueId) });
  qc.removeQueries({ queryKey: issueKeys.reactions(issueId) });
  qc.removeQueries({ queryKey: issueKeys.subscribers(issueId) });
  qc.removeQueries({ queryKey: issueKeys.usage(issueId) });
  qc.removeQueries({ queryKey: issueKeys.attachments(issueId) });
  qc.removeQueries({ queryKey: issueKeys.tasks(issueId) });
  qc.removeQueries({ queryKey: issueKeys.children(wsId, issueId) });
  qc.removeQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });

  qc.invalidateQueries({ queryKey: issueKeys.childProgress(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.list(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.myAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.flatAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
  // Project Gantt 缓存在 `myAll` 之外，所以议题被移除时需要显式刷新——
  // 被删的行可能就是此刻可见的一根排期条。
  qc.invalidateQueries({ queryKey: issueKeys.projectGanttAll(wsId) });
  invalidateDeletedIssueDependentCaches(qc, wsId);

  // Recent Issues 存储持久化到 localStorage、刷新后仍在，所以遗留一个已删除的 id
  // 会让 Cmd+K 命令栏每次打开都打 404。删除 mutation 和 WS 删除事件都会走到这里，
  // 所以一次调用就同时覆盖了“自己删”和“别的客户端删”。
  useRecentIssuesStore.getState().forgetIssue(wsId, issueId);
}
