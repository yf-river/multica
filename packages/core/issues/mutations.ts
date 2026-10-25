// 议题的所有写操作（mutation hook）：创建/更新/删除/批量操作/评论/订阅等。
// 每个 hook 都通过 cache-coordinator 的"规则表"做乐观更新与回滚，
// 让"自己改"与"别人改"（WS 路径）共用同一套缓存传播规则。
import { useRef } from "react";
import { workspaceKeys } from "../workspace/queries";
import type { Workspace } from "../types/workspace";
import { findIssueLocation } from "./cache-helpers";
import { normalizeStatusPatch } from "./status-category";
import {
  hashKey,
  useMutation,
  useQueryClient,
  type QueryClient,
  type QueryKey,
} from "@tanstack/react-query";
import { api } from "../api";
import { issueKeys } from "./queries";
import { projectKeys } from "../projects/queries";
import { inboxKeys, type ArchivedInboxCache } from "../inbox/queries";
import {
  cancelInboxLists,
  isInboxListRequestInFlight,
  onInboxInvalidate,
} from "../inbox/ws-updaters";
import {
  applyIssueChange,
  invalidateIssueDerivatives,
  invalidateLastActivitySortedIssueLists,
  invalidateStaleListKeys,
  rollbackIssueChange,
  type IssueFlatCache,
  type IssueTableRowCache,
} from "./cache-coordinator";
import { issueChangedDims } from "./surface/membership";
import { addIssueToBuckets } from "./cache-helpers";
import {
  cleanupDeletedIssueCaches,
  collectDeletedIssueCacheMetadata,
  invalidateDeletedIssueDependentCaches,
  invalidateDeletedIssueParentCaches,
  invalidateIssueScopedCaches,
  pruneDeletedIssueFromListCaches,
  pruneDeletedIssueFromParentChildrenCaches,
} from "./delete-cache";
import { useWorkspaceId } from "../hooks";
import { useRecentContextStore } from "../chat/recent-context-store";
import { useRecentIssuesStore, useTaskSupplementDraftStore } from "./stores";
import type { InboxItem, Issue, IssueReaction } from "../types";
import type {
  CreateCommentSubIssueManualRequest,
  CreateIssueRequest,
  ListIssuesCache,
  MoveIssueRequest,
  UpdateIssueRequest,
} from "../types";
import type { TimelineEntry, IssueSubscriber, Reaction } from "../types";
import { sortTimelineEntriesAsc } from "./timeline-sort";
import { applyCommentDeletion, removeCommentSubtree } from "./comment-deletion";
import { configStore } from "../config";
import {
  onIssueAuxiliaryRevision,
  invalidateIssueOwnerProjections,
  reconcileIssueFullSnapshotRevision,
} from "./ws-updaters";

// ---------------------------------------------------------------------------
// 共享的 mutation 变量类型——同时被 mutation hooks 和 useMutationState
// 的使用方引用，保证那处类型断言始终同步。
// ---------------------------------------------------------------------------

// 聚合窗口中的任务仍需使用所属工作区的请求头。
function issueMutationWorkspace(qc: QueryClient, wsId: string, id: string): string | undefined {
  let workspaceId = qc.getQueryData<Issue>(issueKeys.detail(wsId, id))?.workspace_id;
  if (!workspaceId) {
    for (const [, data] of qc.getQueriesData<IssueTableRowCache>({ queryKey: issueKeys.tableAll(wsId) })) {
      workspaceId = data?.rows?.find(row => row.issue.id === id)?.issue.workspace_id;
      if (workspaceId) break;
    }
  }
  if (!workspaceId) {
    for (const [, data] of qc.getQueriesData<ListIssuesCache>({ queryKey: issueKeys.list(wsId) })) {
      if (!data) continue;
      workspaceId = findIssueLocation(data, id)?.issue.workspace_id;
      if (workspaceId) break;
    }
  }
  return qc.getQueryData<Workspace[]>(workspaceKeys.list())?.find(w => w.id === (workspaceId ?? wsId))?.slug;
}

function issueMutationGroups(qc: QueryClient, wsId: string, ids: string[], sources?: Map<string, string | undefined>) {
  const groups = new Map<string | undefined, string[]>();
  for (const id of ids) {
    const slug = sources ? sources.get(id) : issueMutationWorkspace(qc, wsId, id);
    groups.set(slug, [...(groups.get(slug) ?? []), id]);
  }
  return [...groups];
}

/** 切换某条评论上的某个表情反应的入参。 */
export type ToggleCommentReactionVars = {
  commentId: string;
  emoji: string;
  existing: Reaction | undefined;
};

/** 切换议题本身的某个表情反应的入参。 */
export type ToggleIssueReactionVars = {
  emoji: string;
  existing: IssueReaction | undefined;
};

/** 更新议题的输入：`id` 加上服务端可接受的全部更新字段。 */
export type UpdateIssueMutationInput = {
  id: string;
  /**
   * 只在拖拽（drag/drop）时出现。乐观补丁里保留 `position`，
   * 而真正发给服务器的请求换成相对锚点（before_id / after_id）。
   */
  move_intent?: Pick<MoveIssueRequest, "before_id" | "after_id">;
} & UpdateIssueRequest;

// ---------------------------------------------------------------------------
// 按状态的加分页（暂无内容，占位说明用）
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 议题增删改（CRUD）
// ---------------------------------------------------------------------------

// Shared by the single and batch update hooks, so the inbox re-read can wait
// for the last of them (see `owedInboxRereads`).
const ISSUE_WRITE = "issue-write";
const issueWriteMutationKey = (wsId: string) => [ISSUE_WRITE, wsId] as const;

// Workspaces whose inbox lists owe a re-read, per client.
const inboxRereadsOwed = new WeakMap<QueryClient, Set<string>>();

// A client's owed re-reads, each paid once its workspace has no issue write
// left in flight. Issued earlier, the re-read could read a pending write's old
// value and land after that write's patch, reverting a saved change. The check
// runs when a write reaches its terminal state, not in onSettled: TanStack
// calls onSettled while the mutation still counts as pending, so two writes
// settling in the same tick would each count the other and both skip
// (MUL-7286).
function owedInboxRereads(qc: QueryClient): Set<string> {
  const existing = inboxRereadsOwed.get(qc);
  if (existing) return existing;
  const owed = new Set<string>();
  inboxRereadsOwed.set(qc, owed);
  qc.getMutationCache().subscribe((event) => {
    if (event.type !== "updated") return;
    if (event.action.type !== "success" && event.action.type !== "error") return;
    const [kind, wsId] = event.mutation.options.mutationKey ?? [];
    if (kind !== ISSUE_WRITE || typeof wsId !== "string") return;
    if (!owed.has(wsId)) return;
    if (qc.isMutating({ mutationKey: issueWriteMutationKey(wsId) }) > 0) return;
    owed.delete(wsId);
    void onInboxInvalidate(qc, wsId);
  });
  return owed;
}

// Inbox side of settling a status / priority write. It patched the inbox rows
// optimistically, so the lists owe a re-read only when the write left them
// behind the server: onMutate interrupted one of their requests (see
// `cancelInboxLists`), a failure restored their rollback snapshot, or a list
// request is still out and may have read the server before this write
// committed. Otherwise the patch was the whole change, and no request is added.
function settleInboxAfterIssueWrite(
  qc: QueryClient,
  wsId: string,
  // Set by onMutate for status / priority writes only.
  inboxWrite: { interrupted: boolean } | undefined,
  failed: boolean,
) {
  if (!inboxWrite) return;
  if (inboxWrite.interrupted || failed || isInboxListRequestInFlight(qc, wsId)) {
    owedInboxRereads(qc).add(wsId);
  }
}

function useIssueCreateMutation<TVariables>(
  mutationFn: (variables: TVariables) => Promise<Issue>,
) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn,
    onSuccess: (newIssue) => {
      for (const [key, data] of qc.getQueriesData<ListIssuesCache>({ queryKey: issueKeys.list(wsId) })) {
        if (data) qc.setQueryData<ListIssuesCache>(key, addIssueToBuckets(data, newIssue));
      }
      // 让刚创建的议题出现在 Cmd+K 的"最近"列表里，不必等用户先打开它一次。
      useRecentIssuesStore.getState().recordVisit(wsId, newIssue.id);
      // 失效父议题的子议题查询，让"子议题列表"立刻反映新创建的子议题。
      if (newIssue.parent_issue_id) {
        qc.invalidateQueries({ queryKey: issueKeys.children(wsId, newIssue.parent_issue_id) });
        qc.invalidateQueries({ queryKey: issueKeys.childProgress(wsId) });
      }
    },
    onSettled: () => {
      // 所有列表类聚合缓存无法从单个议题精确修补，统一失效等重取。
      qc.invalidateQueries({ queryKey: issueKeys.list(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.flatAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.assigneeGroupsAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.myAssigneeGroupsAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.projectGanttAll(wsId) });
      qc.invalidateQueries({ queryKey: projectKeys.all(wsId) });
    },
  });
}

/** 创建一条普通议题（可能带父议题、项目等默认值）的 mutation hook。 */
export function useCreateIssue() {
  return useIssueCreateMutation((data: CreateIssueRequest) => api.createIssue(data));
}

/** 在指定评论下创建子议题的 mutation hook。 */
export function useCreateCommentSubIssue() {
  return useIssueCreateMutation(({
    anchorCommentId,
    data,
  }: {
    anchorCommentId: string;
    data: CreateCommentSubIssueManualRequest;
  }) => api.createCommentSubIssue(anchorCommentId, data));
}

/**
 * 更新（或拖拽移动）单个议题的 mutation hook。
 *
 * 通过 {@link applyIssueChange} 走跨缓存规则表：乐观修补/重分桶、必要时跨视图移除、
 * 无法确定的列表标记过期；onError 回滚、onSuccess 用服务端权威实体做外科手术式对账。
 */
export function useUpdateIssue() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const sources = useRef(new Map<string, string | undefined>());
  return useMutation({
    mutationKey: issueWriteMutationKey(wsId),
    mutationFn: ({ id, move_intent: moveIntent, ...data }: UpdateIssueMutationInput) => {
      if (!moveIntent) return api.updateIssue(id, data, sources.current.get(id));
      const { position: _optimisticPosition, ...target } = data;
      return api.moveIssue(id, { ...target, ...moveIntent }, sources.current.get(id));
    },
    onMutate: ({ id, move_intent: _moveIntent, ...data }) => {
      sources.current.set(id, issueMutationWorkspace(qc, wsId, id));
      // suppress_run / handoff_note 是"写入时"的控制字段，不是议题列。
      // description_base 是合并元数据，而 description 由服务端基于该 base 解析，
      // 因此不能乐观预测。把权威的原始 description 留在缓存里，隐藏的频道媒体
      // 标记才能在紧接着的二次编辑中继续作为 base 使用。mutationFn 仍会发送完整载荷。
      const {
        suppress_run: _suppressRun,
        handoff_note: _handoffNote,
        description: _description,
        description_base: _descriptionBase,
        title_base: _titleBase,
        expected_revision: _expectedRevision,
        ...patch
      } = data;
      // 不等待的 cancelQueries——保证 onMutate 同步执行，缓存更新与 mutate()
      // 发生在同一拍内。若 await 会把执行权让给事件循环，让 @dnd-kit 在乐观更新
      // 落地前先重置视觉状态（拖拽闪烁的根源）。
      qc.cancelQueries({ queryKey: issueKeys.list(wsId) });
      qc.cancelQueries({ queryKey: issueKeys.myAll(wsId) });
      qc.cancelQueries({ queryKey: issueKeys.flatAll(wsId) });
      qc.cancelQueries({ queryKey: issueKeys.tableAll(wsId) });
      const inboxWrite =
        patch.status !== undefined || patch.priority !== undefined
          ? { interrupted: cancelInboxLists(qc, wsId) }
          : undefined;
      const prevDetail = qc.getQueryData<Issue>(issueKeys.detail(wsId, id));
      // 协调器拥有跨缓存规则：卡片已加载且仍属于该列表 → 外科手术式修补/重分桶；
      // 变更让它离开某过滤视图 → 外科手术式移除；服务器结果可能漂移（涉及成员关系
      // 不确定）→ 标记过期 key（在 settle 而非这里失效——飞行中的重取会踩掉乐观状态）。
      const change = applyIssueChange(qc, wsId, id, patch as Partial<Issue>, {
        changed: issueChangedDims(patch, prevDetail),
        baseIssue: prevDetail,
      });

      // 从最新鲜的来源解析 parent_issue_id，以便同步父议题的"子议题列表"缓存
      // （父议题详情页的子议题列表渲染它）。找不到时回退到扫描已加载的 children 缓存——
      // 当用户直接打开某个父议题详情页时，该子议题可能只存在于那里，而不在详情/列表缓存。
      let parentId: string | null =
        prevDetail?.parent_issue_id ??
        change.prevIssue?.parent_issue_id ??
        null;
      if (!parentId) {
        const childrenCaches = qc.getQueriesData<Issue[]>({
          queryKey: [...issueKeys.all(wsId), "children"],
        });
        for (const [key, data] of childrenCaches) {
          if (!data?.some((c) => c.id === id)) continue;
          const candidate = key[key.length - 1];
          if (typeof candidate === "string") {
            parentId = candidate;
            break;
          }
        }
      }
      const prevChildren = parentId
        ? qc.getQueryData<Issue[]>(issueKeys.children(wsId, parentId))
        : undefined;

      if (parentId) {
        // 当本次写入把议题从这个 parentId 改挂走（脱离成独立议题，或挂到别的父议题下），
        // 先从旧父议题的 children 缓存里把它移除。父议题的"子议题列表"直接渲染该数组，
        // 只裸补丁 parent_issue_id: null 会让列表残留一行孤儿，直到 settle 重取才消失。
        // onError 会恢复 prevChildren，所以失败时移除也会回滚。
        const detachedFromParent =
          Object.prototype.hasOwnProperty.call(patch, "parent_issue_id") &&
          patch.parent_issue_id !== parentId;
        qc.setQueryData<Issue[]>(
          issueKeys.children(wsId, parentId),
          (old) =>
            detachedFromParent
              ? old?.filter((c) => c.id !== id)
              : old?.map((c) => (c.id === id ? { ...c, ...normalizeStatusPatch(patch) } : c)),
        );
      }
      return { change, prevChildren, parentId, id, inboxWrite };
    },
    onError: (_err, vars, ctx) => {
      if (ctx) {
        rollbackIssueChange(qc, wsId, ctx.id, ctx.change);
      }
      if (ctx?.parentId && ctx.prevChildren !== undefined) {
        qc.setQueryData(
          issueKeys.children(wsId, ctx.parentId),
          ctx.prevChildren,
        );
      }
      // onMutate 捕获回滚快照之后，可能有更新的远端 revision 已通过 WS 落地。
      // 此时无脑恢复快照可能把更旧的实体暂时放回缓存；所以回滚后刷新每个已加载的
      // 归属投影。revision 冲突最常见，但传输错误/5xx 也有同样的交错风险。
      qc.invalidateQueries({ queryKey: issueKeys.detail(wsId, vars.id) });
      qc.invalidateQueries({ queryKey: issueKeys.list(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.myAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.flatAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
    },
    onSuccess: (serverIssue, vars) => {
      // 用权威的服务端实体"就地修补那一张卡"来对账——而不是失效 + 重取列表。
      // 列表重取正是让一次成功的拖拽闪烁的原因：乐观卡片已经落在正确位置，
      // 重取却把整列替换掉、卡片再次落位。updateIssue 返回完整议题、position 更新
      // 只影响那一行，所以外科手术式修补就是权威对账，且当乐观值与之一致时视觉上零操作。
      //
      // baseIssue = serverIssue：成员关系移动在乐观阶段已处理；对着写入后的实体，
      // 除非服务器强制了不同值，否则 changed dims 都是 false，因此这轮就是它一直以来的
      // 纯外科修补。
      const {
        suppress_run: _suppressRun,
        handoff_note: _handoffNote,
        description_base: _descriptionBase,
        move_intent: _moveIntent,
        id: _id,
        ...intent
      } = vars;
      // 从对账载荷里丢弃 `properties`：这个袋子由"属性变更管线"独占
      // （单 key 原子写 + 自己的乐观状态）。若在并发属性写完成前捕获的
      // UpdateIssue 快照落到这里，会覆盖掉更新的袋子（clean-room review F3
      // 的响应排序竞态）。
      const { properties: _staleBag, ...reconcilable } = serverIssue;
      const reconcile = applyIssueChange(qc, wsId, serverIssue.id, reconcilable as typeof serverIssue, {
        changed: issueChangedDims(intent, serverIssue),
        baseIssue: serverIssue,
        // HTTP 响应可能晚于更新的 WS 事件到达。只把已提交的快照对账进那些
        // "尚未推进到它之后"的投影；否则一份较旧的、但成功的响应会在缓存里
        // 撤销之后发生的远端写入。
        acceptCurrent: (current) =>
          current.revision === undefined ||
          (serverIssue.revision !== undefined &&
            serverIssue.revision > current.revision),
      });
      reconcileIssueFullSnapshotRevision(
        qc,
        wsId,
        serverIssue.id,
        serverIssue.revision,
      );
      // 服务器已提交——现在可以安全地冲刷它报告的任何漂移。
      invalidateStaleListKeys(qc, reconcile.staleKeys);
    },
    onSettled: (_data, err, vars, ctx) => {
      settleInboxAfterIssueWrite(qc, wsId, ctx?.inboxWrite, err !== null);
      sources.current.delete(vars.id);
      // 议题自己的列表 + 详情缓存在 onSuccess / onError 里已做外科手术式对账，
      // 所以这里刻意不失效它们——settle 时整表重取正是拖拽闪烁的来源。只有无法从
      // 单个议题修补出来的聚合缓存需要刷新，外加协调器标记为漂移的特定列表 key
      // （成员关系不确定、在已加载窗口之外进出、桶计数漂移）。
      // 这些过期 key 是旧版"项目移动就全量失效 myAll"安全网的精准替代（MUL-3669 / #4548）：
      // 旧项目的已加载列表在 onMutate 里已把卡片移除，这里只重取真正无法判定的列表。
      invalidateIssueDerivatives(qc, wsId, {
        statusOrProjectChanged:
          vars.status !== undefined ||
          Object.prototype.hasOwnProperty.call(vars, "project_id"),
      });
      qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
      if (ctx) {
        invalidateStaleListKeys(qc, ctx.change.staleKeys);
      }
      // 当描述编辑器绑定了新上传的附件时，刷新该议题的附件缓存——描述编辑器
      // 读取 `issueAttachments` 来解析文本预览的 Eye 门；与其它 mutation 不同，
      // 这个载荷会改动附件关联表。
      if (vars.attachment_ids?.length) {
        qc.invalidateQueries({ queryKey: issueKeys.attachments(vars.id) });
      }
      // 失效旧父议题的 children 缓存。
      if (ctx?.parentId) {
        qc.invalidateQueries({
          queryKey: issueKeys.children(wsId, ctx.parentId),
        });
        qc.invalidateQueries({ queryKey: issueKeys.childProgress(wsId) });
      }
      // parent_issue_id 变化时，失效新父议题的 children 缓存。
      const newParentId = vars.parent_issue_id;
      if (newParentId && newParentId !== ctx?.parentId) {
        qc.invalidateQueries({
          queryKey: issueKeys.children(wsId, newParentId),
        });
        qc.invalidateQueries({ queryKey: issueKeys.childProgress(wsId) });
      }
      // 只有当父子关系真的变化时才失效"批量 children"缓存。WS 路径（ws-updaters.ts）
      // 无条件失效是因为它不知道服务器改了哪些东西；而这里 onMutate 已乐观修补了
      // issueKeys.children(parent)，只有当父关系本身移动过才需要额外冲刷。
      if (ctx?.parentId || newParentId) {
        qc.invalidateQueries({ queryKey: issueKeys.childrenByParentsAll(wsId) });
      }
    },
  });
}

/**
 * 删除单个议题的 mutation hook：乐观地修剪列表/子议题缓存并移除详情，
 * 失败时整体还原，成功后做全量缓存清理。
 */
export function useDeleteIssue() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const sources = useRef(new Map<string, string | undefined>());
  return useMutation({
    mutationFn: (id: string) => api.deleteIssue(id, sources.current.get(id)),
    onMutate: async (id) => {
      sources.current.set(id, issueMutationWorkspace(qc, wsId, id));
      await Promise.all([
        qc.cancelQueries({ queryKey: issueKeys.list(wsId) }),
        qc.cancelQueries({ queryKey: issueKeys.myAll(wsId) }),
        qc.cancelQueries({ queryKey: issueKeys.flatAll(wsId) }),
      ]);
      const metadata = collectDeletedIssueCacheMetadata(qc, wsId, id);
      await Promise.all(
        metadata.parentIssueIds.map((parentId) =>
          qc.cancelQueries({ queryKey: issueKeys.children(wsId, parentId) }),
        ),
      );
      // 在动手前把所有受影响的缓存快照留存，失败时逐一还原。
      const prevLists = qc.getQueriesData<ListIssuesCache>({ queryKey: issueKeys.list(wsId) });
      const prevMyLists = qc.getQueriesData<ListIssuesCache>({
        queryKey: issueKeys.myAll(wsId),
      });
      const prevFlatLists = qc.getQueriesData<IssueFlatCache>({
        queryKey: issueKeys.flatAll(wsId),
      });
      const prevDetail = qc.getQueryData<Issue>(issueKeys.detail(wsId, id));
      const prevChildren = new Map<string, Issue[] | undefined>();
      for (const parentId of metadata.parentIssueIds) {
        prevChildren.set(
          parentId,
          qc.getQueryData<Issue[]>(issueKeys.children(wsId, parentId)),
        );
      }

      // 乐观移除：从各列表/子议题缓存剔除该议题并移除详情缓存。
      pruneDeletedIssueFromListCaches(qc, wsId, id);
      pruneDeletedIssueFromParentChildrenCaches(qc, wsId, id, metadata);
      qc.removeQueries({ queryKey: issueKeys.detail(wsId, id) });
      return {
        id,
        metadata,
        prevLists,
        prevMyLists,
        prevFlatLists,
        prevDetail,
        prevChildren,
      };
    },
    onError: (_err, _id, ctx) => {
      // 失败：把乐观阶段清掉的东西原样放回缓存。
      if (ctx?.prevLists) {
        for (const [key, snapshot] of ctx.prevLists) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevMyLists) {
        for (const [key, snapshot] of ctx.prevMyLists) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevFlatLists) {
        for (const [key, snapshot] of ctx.prevFlatLists) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevDetail) {
        qc.setQueryData(issueKeys.detail(wsId, ctx.id), ctx.prevDetail);
      }
      if (ctx?.prevChildren) {
        for (const [parentId, snapshot] of ctx.prevChildren) {
          qc.setQueryData(issueKeys.children(wsId, parentId), snapshot);
        }
      }
    },
    onSuccess: (_data, id, ctx) => {
      // 让 Cmd+K 的"最近上下文"遗忘这个已删除的议题，避免点开即 404。
      useRecentContextStore.getState().forgetContext(wsId, { type: "issue", id });
      cleanupDeletedIssueCaches(qc, wsId, id, ctx?.metadata);
    },
    onSettled: (_data, _err, _id, ctx) => {
      sources.current.delete(_id);
      // 无法从单议题增量算出的聚合缓存统一失效重取。
      qc.invalidateQueries({ queryKey: issueKeys.list(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.flatAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.assigneeGroupsAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.myAssigneeGroupsAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.projectGanttAll(wsId) });
      qc.invalidateQueries({ queryKey: projectKeys.all(wsId) });
      if (ctx?.metadata) invalidateDeletedIssueParentCaches(qc, wsId, ctx.metadata);
    },
  });
}

/**
 * 批量更新多个议题的 mutation hook（例如批量改状态/优先级/负责人/项目）。
 * 逐个议题跑同一套协调器规则，因此批量编辑在工作区看板与每个过滤的 myList
 * 视图上的修补/移除行为与单条更新完全一致。
 */
export function useBatchUpdateIssues() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const sources = useRef(new Map<string, string | undefined>());
  return useMutation({
    mutationKey: issueWriteMutationKey(wsId),
    mutationFn: ({
      ids,
      updates,
    }: {
      ids: string[];
      updates: UpdateIssueRequest;
    }) => Promise.all(issueMutationGroups(qc, wsId, ids, sources.current).map(([slug, group]) => api.batchUpdateIssues(group, updates, slug))).then(results => ({ updated: results.reduce((sum, r) => sum + r.updated, 0) })),
    onMutate: async ({ ids, updates }) => {
      for (const id of ids) sources.current.set(id, issueMutationWorkspace(qc, wsId, id));
      // 控制字段与描述合并字段不适合做乐观缓存修补。服务端会基于 description_base
      // 解析 description，因此在重取返回提交结果前，保留权威的原始 description
      // （含媒体标记）。
      const {
        suppress_run: _suppressRun,
        handoff_note: _handoffNote,
        description: _description,
        description_base: _descriptionBase,
        ...patch
      } = updates;
      await qc.cancelQueries({ queryKey: issueKeys.list(wsId) });
      await qc.cancelQueries({ queryKey: issueKeys.myAll(wsId) });
      await qc.cancelQueries({ queryKey: issueKeys.flatAll(wsId) });
      await qc.cancelQueries({ queryKey: issueKeys.tableAll(wsId) });
      const inboxWrite =
        patch.status !== undefined || patch.priority !== undefined
          ? { interrupted: cancelInboxLists(qc, wsId) }
          : undefined;

      // 让每个议题都经过协调器——与单条更新同一张规则表，因此批量编辑会在工作区
      // 看板 AND 每个过滤的 myList 视图上做一致的修补/移除。
      // 快照按缓存 key 取"第一个胜出"：第一个议题应用后缓存已带部分补丁，所以每个
      // key 只有第一份快照是干净可回滚的。
      const prevListByHash = new Map<string, [QueryKey, ListIssuesCache]>();
      const prevFlatListByHash = new Map<string, [QueryKey, IssueFlatCache]>();
      const prevTableRowByHash = new Map<
        string,
        [QueryKey, IssueTableRowCache]
      >();
      const prevDetailById = new Map<string, Issue>();
      let prevInboxList: InboxItem[] | undefined;
      let prevArchivedInboxCaches: [QueryKey, ArchivedInboxCache | undefined][] | undefined;
      const staleKeys: QueryKey[] = [];
      for (const id of ids) {
        const base = qc.getQueryData<Issue>(issueKeys.detail(wsId, id));
        const change = applyIssueChange(qc, wsId, id, patch as Partial<Issue>, {
          changed: issueChangedDims(patch, base),
          baseIssue: base,
        });
        for (const [key, snapshot] of change.prevLists) {
          const hash = hashKey(key);
          if (!prevListByHash.has(hash)) prevListByHash.set(hash, [key, snapshot]);
        }
        for (const [key, snapshot] of change.prevFlatLists) {
          const hash = hashKey(key);
          if (!prevFlatListByHash.has(hash)) {
            prevFlatListByHash.set(hash, [key, snapshot]);
          }
        }
        for (const [key, snapshot] of change.prevTableRows) {
          const hash = hashKey(key);
          if (!prevTableRowByHash.has(hash)) {
            prevTableRowByHash.set(hash, [key, snapshot]);
          }
        }
        if (change.prevDetail) prevDetailById.set(id, change.prevDetail);
        if (prevInboxList === undefined && change.prevInboxList !== undefined) {
          prevInboxList = change.prevInboxList;
        }
        if (
          prevArchivedInboxCaches === undefined &&
          change.prevArchivedInboxCaches !== undefined
        ) {
          prevArchivedInboxCaches = change.prevArchivedInboxCaches;
        }
        staleKeys.push(...change.staleKeys);
      }

      // 把乐观补丁镜像进任何已加载的 children 缓存，让父议题详情页上的子议题行
      // 也立即反映本次变更。
      const idSet = new Set(ids);
      const childrenCaches = qc.getQueriesData<Issue[]>({
        queryKey: [...issueKeys.all(wsId), "children"],
      });
      const prevChildren = new Map<string, Issue[] | undefined>();
      const affectedParentIds = new Set<string>();
      for (const [key, data] of childrenCaches) {
        if (!data?.some((c) => idSet.has(c.id))) continue;
        const parentId = key[key.length - 1];
        if (typeof parentId !== "string") continue;
        affectedParentIds.add(parentId);
        prevChildren.set(parentId, data);
        qc.setQueryData<Issue[]>(issueKeys.children(wsId, parentId), (old) =>
          old?.map((c) => (idSet.has(c.id) ? { ...c, ...normalizeStatusPatch(patch) } : c)),
        );
      }

      return {
        prevLists: [...prevListByHash.values()],
        prevFlatLists: [...prevFlatListByHash.values()],
        prevTableRows: [...prevTableRowByHash.values()],
        prevDetailById,
        prevInboxList,
        prevArchivedInboxCaches,
        inboxWrite,
        staleKeys,
        prevChildren,
        affectedParentIds,
      };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prevLists) {
        for (const [key, snapshot] of ctx.prevLists) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevFlatLists) {
        for (const [key, snapshot] of ctx.prevFlatLists) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevTableRows) {
        for (const [key, snapshot] of ctx.prevTableRows) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevDetailById) {
        for (const [id, snapshot] of ctx.prevDetailById) {
          qc.setQueryData(issueKeys.detail(wsId, id), snapshot);
        }
      }
      if (ctx?.prevInboxList !== undefined) {
        qc.setQueryData(inboxKeys.list(wsId), ctx.prevInboxList);
      }
      for (const [key, snapshot] of ctx?.prevArchivedInboxCaches ?? []) {
        qc.setQueryData(key, snapshot);
      }
      if (ctx?.prevChildren) {
        for (const [parentId, snapshot] of ctx.prevChildren) {
          qc.setQueryData(issueKeys.children(wsId, parentId), snapshot);
        }
      }
    },
    onSettled: (_data, err, _vars, ctx) => {
      settleInboxAfterIssueWrite(qc, wsId, ctx?.inboxWrite, err !== null);
      for (const id of _vars.ids) sources.current.delete(id);
      // 刻意不失效 issueKeys.list / myAll：上面 onMutate 的一轮已经是已加载分桶看板
      // 的完整外科对账，settle 时整板重取只会把单条更新刚消除的闪烁带回来。下面只刷新
      // 无法从单议题补丁重算的聚合/分组缓存，外加协调器标记为漂移的特定 key——
      // 即旧版"项目移动就全量失效 myAll"安全网的精准替代（MUL-3669 / #4548）。
      invalidateIssueDerivatives(qc, wsId, {
        statusOrProjectChanged:
          _vars.updates.status !== undefined ||
          Object.prototype.hasOwnProperty.call(_vars.updates, "project_id"),
      });
      qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
      if (ctx) {
        invalidateStaleListKeys(qc, ctx.staleKeys);
      }
      if (ctx?.affectedParentIds && ctx.affectedParentIds.size > 0) {
        for (const parentId of ctx.affectedParentIds) {
          qc.invalidateQueries({
            queryKey: issueKeys.children(wsId, parentId),
          });
        }
        qc.invalidateQueries({ queryKey: issueKeys.childProgress(wsId) });
      }
    },
  });
}

/**
 * 批量删除多个议题的 mutation hook。先按每个被删议题收集涉及的父议题，
 * 乐观移除后失败整体还原；全部删除成功才做清理，部分失败（`deleted < length`）
 * 则还原并改为失效。
 */
export function useBatchDeleteIssues() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const sources = useRef(new Map<string, string | undefined>());
  return useMutation({
    mutationFn: (ids: string[]) => Promise.all(issueMutationGroups(qc, wsId, ids, sources.current).map(([slug, group]) => api.batchDeleteIssues(group, slug))).then(results => ({ deleted: results.reduce((sum, r) => sum + r.deleted, 0) })),
    onMutate: async (ids) => {
      for (const id of ids) sources.current.set(id, issueMutationWorkspace(qc, wsId, id));
      await Promise.all([
        qc.cancelQueries({ queryKey: issueKeys.list(wsId) }),
        qc.cancelQueries({ queryKey: issueKeys.myAll(wsId) }),
        qc.cancelQueries({ queryKey: issueKeys.flatAll(wsId) }),
      ]);
      // 为每个待删议题收集它涉及的父议题 id（扫描详情/列表/children 各类缓存）。
      const metadataById = new Map(
        ids.map((id) => [
          id,
          collectDeletedIssueCacheMetadata(qc, wsId, id),
        ]),
      );
      const parentIssueIds = new Set<string>();
      for (const metadata of metadataById.values()) {
        for (const parentId of metadata.parentIssueIds) {
          parentIssueIds.add(parentId);
        }
      }
      await Promise.all(
        Array.from(parentIssueIds).map((parentId) =>
          qc.cancelQueries({ queryKey: issueKeys.children(wsId, parentId) }),
        ),
      );
      const prevLists = qc.getQueriesData<ListIssuesCache>({ queryKey: issueKeys.list(wsId) });
      const prevMyLists = qc.getQueriesData<ListIssuesCache>({
        queryKey: issueKeys.myAll(wsId),
      });
      const prevFlatLists = qc.getQueriesData<IssueFlatCache>({
        queryKey: issueKeys.flatAll(wsId),
      });
      const prevChildren = new Map<string, Issue[] | undefined>();
      for (const parentId of parentIssueIds) {
        prevChildren.set(
          parentId,
          qc.getQueryData<Issue[]>(issueKeys.children(wsId, parentId)),
        );
      }

      for (const id of ids) {
        const metadata = metadataById.get(id);
        pruneDeletedIssueFromListCaches(qc, wsId, id);
        if (metadata) {
          pruneDeletedIssueFromParentChildrenCaches(qc, wsId, id, metadata);
        }
      }
      return {
        prevLists,
        prevMyLists,
        prevFlatLists,
        prevChildren,
        parentIssueIds,
        metadataById,
      };
    },
    onError: (_err, _ids, ctx) => {
      if (ctx?.prevLists) {
        for (const [key, snapshot] of ctx.prevLists) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevMyLists) {
        for (const [key, snapshot] of ctx.prevMyLists) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevFlatLists) {
        for (const [key, snapshot] of ctx.prevFlatLists) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevChildren) {
        for (const [parentId, snapshot] of ctx.prevChildren) {
          qc.setQueryData(issueKeys.children(wsId, parentId), snapshot);
        }
      }
    },
    onSuccess: (data, ids, ctx) => {
      if (data.deleted === ids.length) {
        const { forgetContext } = useRecentContextStore.getState();
        for (const id of ids) {
          forgetContext(wsId, { type: "issue", id });
          cleanupDeletedIssueCaches(qc, wsId, id, ctx?.metadataById.get(id));
        }
        return;
      }

      if (ctx?.prevLists) {
        for (const [key, snapshot] of ctx.prevLists) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevMyLists) {
        for (const [key, snapshot] of ctx.prevMyLists) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevFlatLists) {
        for (const [key, snapshot] of ctx.prevFlatLists) {
          qc.setQueryData(key, snapshot);
        }
      }
      if (ctx?.prevChildren) {
        for (const [parentId, snapshot] of ctx.prevChildren) {
          qc.setQueryData(issueKeys.children(wsId, parentId), snapshot);
        }
      }
      for (const id of ids) {
        invalidateIssueScopedCaches(qc, wsId, id);
      }
      qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
      invalidateDeletedIssueDependentCaches(qc, wsId);
    },
    onSettled: (_data, _err, _ids, ctx) => {
      for (const id of _ids) sources.current.delete(id);
      qc.invalidateQueries({ queryKey: issueKeys.list(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.flatAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.assigneeGroupsAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.myAssigneeGroupsAll(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.projectGanttAll(wsId) });
      qc.invalidateQueries({ queryKey: projectKeys.all(wsId) });
      if (ctx?.parentIssueIds && ctx.parentIssueIds.size > 0) {
        invalidateDeletedIssueParentCaches(qc, wsId, {
          parentIssueIds: Array.from(ctx.parentIssueIds),
        });
      }
    },
  });
}

// ---------------------------------------------------------------------------
// 评论 / 时间线
// ---------------------------------------------------------------------------

/** 议题时间线缓存的元素类型（`TimelineEntry` 数组）。 */
type TimelineCache = TimelineEntry[];

/**
 * 在议题下创建一条评论的 mutation hook。成功时把服务端返回的评论拼成
 * TimelineEntry 就地插入时间线缓存，并同步议题的辅助投影与按活跃度排序的列表。
 * @param issueId 所属议题 id。
 */
export function useCreateComment(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({
      content,
      type,
      parentId,
      attachmentIds,
      suppressAgentIds,
    }: {
      content: string;
      type?: string;
      parentId?: string;
      attachmentIds?: string[];
      suppressAgentIds?: string[];
    }) => api.createComment(issueId, content, type, parentId, attachmentIds, suppressAgentIds),
    onSuccess: (comment) => {
      if (comment.issue_revision) {
        onIssueAuxiliaryRevision(qc, wsId, issueId, comment.issue_revision);
      } else {
        invalidateIssueOwnerProjections(qc, wsId, issueId);
      }
      invalidateLastActivitySortedIssueLists(qc, wsId);
      const entry: TimelineEntry = {
        type: "comment",
        id: comment.id,
        actor_type: comment.author_type,
        actor_id: comment.author_id,
        content: comment.content,
        parent_id: comment.parent_id,
        comment_type: comment.type,
        reactions: comment.reactions ?? [],
        attachments: comment.attachments ?? [],
        created_at: comment.created_at,
        updated_at: comment.updated_at,
      };
      // 按 id 去重：广播路径上 `comment:created` 的 WS 事件可能已在本次
      // onSuccess 触发前把这条例程加进缓存。若已存在则跳过追加。
      qc.setQueryData<TimelineCache>(issueKeys.timeline(issueId), (old) => {
        if (!old) return [entry];
        if (old.some((e) => e.id === entry.id)) return old;
        return sortTimelineEntriesAsc([...old, entry]);
      });
      // 发评论这件事本身会改变触发器答案（入队的任务现在会对后续触发去重），
      // 所以这条议题缓存的触发预览在创建落地的瞬间就已过期。
      qc.invalidateQueries({ queryKey: issueKeys.commentTriggerPreview(issueId) });
    },
    // 没有 onSettled 失效：成功创建后，`comment:created` 的 WS 广播会保持时间线
    // 缓存新鲜；若连接断开，useIssueTimeline 里的重连恢复逻辑会做失效。
    // 每次提交都整表重取会把每条记录的引用换掉，逼着所有被 memo 化的 CommentCard
    // 子树重渲染（AI 流式输出时，兄弟线程会可见地闪一下）。
  });
}

/**
 * 编辑一条评论的 mutation hook：乐观替换时间线里的正文并过滤附件，
 * 成功后同步辅助投影与活跃度列表，最终失效时间线缓存以对账。
 * @param issueId 所属议题 id。
 */
export function useUpdateComment(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({
      commentId,
      content,
      attachmentIds,
      suppressAgentIds,
      contentBase,
      expectedRevision,
    }: {
      commentId: string;
      content: string;
      attachmentIds: string[];
      suppressAgentIds?: string[];
      contentBase?: string;
      expectedRevision?: number;
    }) => api.updateComment(commentId, content, attachmentIds, suppressAgentIds, contentBase, expectedRevision),
    onMutate: async ({ commentId, content, attachmentIds }) => {
      await qc.cancelQueries({ queryKey: issueKeys.timeline(issueId) });
      const prev = qc.getQueryData<TimelineCache>(issueKeys.timeline(issueId));
      const kept = new Set(attachmentIds);
      qc.setQueryData<TimelineCache>(issueKeys.timeline(issueId), (old) =>
        old?.map((e) =>
          e.id === commentId
            ? { ...e, content, attachments: e.attachments?.filter((a) => kept.has(a.id)) }
            : e,
        ),
      );
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev !== undefined) {
        qc.setQueryData(issueKeys.timeline(issueId), ctx.prev);
      }
    },
    onSuccess: (comment) => {
      if (comment.issue_revision) {
        onIssueAuxiliaryRevision(qc, wsId, issueId, comment.issue_revision);
      } else {
        // 混版本回退：旧版后端可能已提交聚合 revision 但没返回它。
        invalidateIssueOwnerProjections(qc, wsId, issueId);
      }
      invalidateLastActivitySortedIssueLists(qc, wsId);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
    },
  });
}

/**
 * 删除一条评论的 mutation hook。乐观移除时会沿 parent_id 级联把整棵回复子树
 * （后代评论）一并清出缓存，失败则整体还原。
 * @param issueId 所属议题 id。
 */
export function useDeleteComment(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    // The capability is read when the delete runs, so the route matches the
    // copy the confirmation showed. Older servers delete the replies too.
    mutationFn: async (commentId: string) => {
      const keepReplies = configStore.getState().commentDeleteKeepRepliesSupported;
      await api.deleteComment(commentId, { keepReplies });
      return keepReplies;
    },
    // Not optimistic: whether the comment disappears or stays as a tombstone
    // depends on replies only the server sees for certain (#8296). Once it
    // confirms, mirror its outcome; realtime events and the settle refetch
    // reconcile the rest.
    onSuccess: (keptReplies, commentId) => {
      qc.setQueryData<TimelineCache>(issueKeys.timeline(issueId), (old) => {
        if (!old) return old;
        return keptReplies
          ? applyCommentDeletion(old, commentId, new Date().toISOString())
          : removeCommentSubtree(old, commentId);
      });
      // The endpoint remains 204 for compatibility, so the local caller has
      // no body carrying issue_revision. The realtime event will narrow this
      // with its revision when connected; this is the no-WS safety net.
      invalidateIssueOwnerProjections(qc, wsId, issueId);
      invalidateLastActivitySortedIssueLists(qc, wsId);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
    },
  });
}

// 与 `commentId` 同属一条线程的所有评论 id——线程根加上每个后代。
// 镜像服务端 ClearOtherThreadResolutions 的线程遍历，使"标记解决"的乐观更新能
// 与后端一样精确清除同级解决状态，而不是在重取落地前短暂地显示两条解决。
function collectThreadCommentIds(
  entries: TimelineCache,
  commentId: string,
): Set<string> {
  const byId = new Map<string, TimelineEntry>();
  for (const e of entries) {
    if (e.type === "comment") byId.set(e.id, e);
  }
  // 向上走到线程根（带环保护，防御畸形的 parent 链）。
  let rootId = commentId;
  const guard = new Set<string>();
  let cur = byId.get(commentId);
  while (cur?.parent_id && byId.has(cur.parent_id) && !guard.has(cur.id)) {
    guard.add(cur.id);
    rootId = cur.parent_id;
    cur = byId.get(cur.parent_id);
  }
  // 再自顶向下展开整棵子树。
  const childrenByParent = new Map<string, string[]>();
  for (const e of byId.values()) {
    if (e.parent_id) {
      const list = childrenByParent.get(e.parent_id) ?? [];
      list.push(e.id);
      childrenByParent.set(e.parent_id, list);
    }
  }
  const ids = new Set<string>([rootId]);
  const stack = [rootId];
  while (stack.length > 0) {
    const id = stack.pop()!;
    for (const child of childrenByParent.get(id) ?? []) {
      if (!ids.has(child)) {
        ids.add(child);
        stack.push(child);
      }
    }
  }
  return ids;
}

/**
 * 把一条评论标记为已解决 / 取消解决的 mutation hook。
 * 乐观更新会镜像服务端"同线程只保留一条解决"的规则：标记解决时清除线程内其它
 * 解决状态；取消解决只清自己的行。
 * @param issueId 所属议题 id。
 */
export function useResolveComment(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ commentId, resolved }: { commentId: string; resolved: boolean }) =>
      resolved ? api.resolveComment(commentId) : api.unresolveComment(commentId),
    onMutate: async ({ commentId, resolved }) => {
      await qc.cancelQueries({ queryKey: issueKeys.timeline(issueId) });
      const prev = qc.getQueryData<TimelineCache>(issueKeys.timeline(issueId));
      qc.setQueryData<TimelineCache>(issueKeys.timeline(issueId), (old) => {
        if (!old) return old;
        // 标记解决会让这条评论成为其线程里的唯一解决，因此镜像服务端
        // （ClearOtherThreadResolutions），把同线程的其它解决清掉。不做这一步，
        // 缓存会同时显示两条解决直到 settle 重取——正是"单解决"修复要消除的闪烁。
        // 取消解决只清自己的行。
        const threadIds = resolved ? collectThreadCommentIds(old, commentId) : null;
        return old.map((e) => {
          if (e.id === commentId) {
            return {
              ...e,
              resolved_at: resolved ? new Date().toISOString() : null,
              resolved_by_type: resolved ? e.resolved_by_type ?? null : null,
              resolved_by_id: resolved ? e.resolved_by_id ?? null : null,
            };
          }
          if (resolved && e.resolved_at && threadIds?.has(e.id)) {
            return { ...e, resolved_at: null, resolved_by_type: null, resolved_by_id: null };
          }
          return e;
        });
      });
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev !== undefined) {
        qc.setQueryData(issueKeys.timeline(issueId), ctx.prev);
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
    },
  });
}

/**
 * 切换某条评论上的表情反应（有则移除、无则添加）的 mutation hook。
 * @param issueId 所属议题 id。
 */
export function useToggleCommentReaction(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationKey: ["toggleCommentReaction", issueId] as const,
    mutationFn: async ({
      commentId,
      emoji,
      existing,
    }: ToggleCommentReactionVars) => {
      if (existing) {
        await api.removeReaction(commentId, emoji);
        return null;
      }
      return api.addReaction(commentId, emoji);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
    },
  });
}

// ---------------------------------------------------------------------------
// 议题级表情反应
// ---------------------------------------------------------------------------

/**
 * 切换议题本身某个表情反应（有则移除、无则添加）的 mutation hook。
 * @param issueId 所属议题 id。
 */
export function useToggleIssueReaction(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationKey: ["toggleIssueReaction", issueId] as const,
    mutationFn: async ({
      emoji,
      existing,
    }: ToggleIssueReactionVars) => {
      if (existing) {
        await api.removeIssueReaction(issueId, emoji);
        return null;
      }
      return api.addIssueReaction(issueId, emoji);
    },
    onSuccess: (reaction) => {
      onIssueAuxiliaryRevision(qc, wsId, issueId, reaction?.issue_revision);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: issueKeys.reactions(issueId) });
    },
  });
}

// ---------------------------------------------------------------------------
// 议题订阅者（关注人）
// ---------------------------------------------------------------------------

/**
 * 订阅 / 取消订阅议题的 mutation hook（乐观更新订阅者列表，失败回滚）。
 * @param issueId 所属议题 id。
 */
export function useToggleIssueSubscriber(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      userId,
      userType,
      subscribed,
    }: {
      userId: string;
      userType: "member" | "agent";
      subscribed: boolean;
    }) => {
      if (subscribed) {
        await api.unsubscribeFromIssue(issueId, userId, userType);
      } else {
        await api.subscribeToIssue(issueId, userId, userType);
      }
    },
    onMutate: async ({ userId, userType, subscribed }) => {
      await qc.cancelQueries({ queryKey: issueKeys.subscribers(issueId) });
      const prev = qc.getQueryData<IssueSubscriber[]>(
        issueKeys.subscribers(issueId),
      );

      if (subscribed) {
        qc.setQueryData<IssueSubscriber[]>(
          issueKeys.subscribers(issueId),
          (old) =>
            old?.filter(
              (s) => !(s.user_id === userId && s.user_type === userType),
            ),
        );
      } else {
        const temp: IssueSubscriber = {
          issue_id: issueId,
          user_type: userType,
          user_id: userId,
          reason: "manual",
          created_at: new Date().toISOString(),
        };
        qc.setQueryData<IssueSubscriber[]>(
          issueKeys.subscribers(issueId),
          (old) => {
            if (
              old?.some(
                (s) => s.user_id === userId && s.user_type === userType,
              )
            )
              return old;
            return [...(old ?? []), temp];
          },
        );
      }
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev)
        qc.setQueryData(issueKeys.subscribers(issueId), ctx.prev);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: issueKeys.subscribers(issueId) });
    },
  });
}

/**
 * 取消订阅一个议题 AND 它的整棵子树（MUL-5483）。不做乐观更新：它写入的是
 * 未知数量其它议题的订阅者列表，没有可确定修补的目标——改为失效所有订阅者查询，
 * 让服务器当唯一真相来源。
 * @param issueId 根议题 id。
 */
export function useUnsubscribeFromIssueSubtree(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      userId,
      userType,
    }: {
      userId: string;
      userType: "member" | "agent";
    }) => {
      await api.unsubscribeFromIssueSubtree(issueId, userId, userType);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: issueKeys.subscribersAll() });
    },
  });
}

export function useCreateTaskSupplement(issueId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ taskId, content, clientRequestId }: {
      taskId: string;
      content: string;
      clientRequestId: string;
    }) => api.createTaskSupplement(issueId, taskId, content, clientRequestId),
    onSuccess: (comment, { taskId, clientRequestId }) => {
      // Re-anchoring the run can unmount its composer before this response.
      // Clear the submitted draft here, without discarding any newer edits.
      const drafts = useTaskSupplementDraftStore.getState();
      if (drafts.drafts[taskId]?.clientRequestId === clientRequestId) {
        drafts.clear(taskId);
      }
      const entry: TimelineEntry = {
        type: "comment",
        id: comment.id,
        actor_type: comment.author_type,
        actor_id: comment.author_id,
        content: comment.content,
        parent_id: comment.parent_id,
        comment_type: comment.type,
        reactions: comment.reactions ?? [],
        attachments: comment.attachments ?? [],
        created_at: comment.created_at,
        updated_at: comment.updated_at,
        supplement_task_id: comment.supplement_task_id,
        supplement_status: comment.supplement_status,
        supplement_failure_reason: comment.supplement_failure_reason,
        supplement_delivered_at: comment.supplement_delivered_at,
      };
      client.setQueryData<TimelineCache>(issueKeys.timeline(issueId), (old) => {
        if (!old) return [entry];
        if (old.some((item) => item.id === entry.id)) return old;
        return sortTimelineEntriesAsc([...old, entry]);
      });
      client.invalidateQueries({ queryKey: issueKeys.tasks(issueId) });
    },
  });
}

export function useRetryTaskSupplement(issueId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ taskId, commentId }: { taskId: string; commentId: string }) =>
      api.retryTaskSupplement(issueId, taskId, commentId),
    onSettled: () => {
      client.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
      client.invalidateQueries({ queryKey: issueKeys.tasks(issueId) });
    },
  });
}

export function useRetryIssueRun(issueId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (taskId: string) => api.rerunIssue(issueId, taskId),
    onSuccess: () => client.invalidateQueries({ queryKey: issueKeys.tasks(issueId) }),
  });
}
