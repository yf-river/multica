// 本文件封装「issue 状态目录」的各类变更：创建、更新（改名/改色/拖动重排）、
// 归档、类别内重排。变更成功后不做失效——统一靠 `issue_status:changed` 实时
// 事件刷新（详见下方说明）。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { compareIssueStatusEntries, issueStatusKeys } from "./queries";
import type {
  CreateIssueStatusRequest,
  IssueStatusCategory,
  IssueStatusEntry,
  ListIssueStatusesResponse,
  UpdateIssueStatusRequest,
} from "../types";

/**
 * 目录变更（MUL-6243）。
 *
 * 没有任何变更会在成功后失效目录。刷新靠的是 `issue_status:changed` 实时
 * 事件——它会同时到达发起写入的那个标签页和其他所有标签页——所以一次目录
 * 编辑对每个客户端恰好只花一次目录读取。若这里也失效，发起写入的管理员会
 * 成为唯一一个读两次的客户端。(MUL-6458)
 *
 * 那次重新拉取正是让缓存收敛的机制，而且按构造它的顺序必然正确：事件在写入
 * 提交后才发布，所以它触发的重拉只可能读到提交后的状态。
 *
 * 这也是为什么没有变更会把「响应体」写进缓存。一个响应只对「那次写入当时」的
 * 该行有权威性，而不是对「现在」——两条通道相互独立，所以慢响应可能落在某次
 * 已经捡到别人后续写入的重拉之后，悄悄把目录滚回一个谁都不会再纠正的状态。
 * 下面每次本地写入，选的都是在「期间无论还落了什么」都依然成立的东西：
 *
 * - 一个乐观的字段补丁（改名、改色、重排），
 * - 插入一行别人不可能已经见过的数据（创建），
 * - 一个单调的旗标（归档——没有取消归档接口，服务端也拒绝编辑归档行，所以
 *   把它置位永远不可能过期）。
 *
 * 失败时保留失效逻辑，这并非为了对称而对称：409 通常意味着「这个客户端手里的
 * 目录才是旧的」——名字被别人占了，或正在拖动的行被归档了。
 */

/** 读取并操作目录缓存的公共逻辑，供下面的变更 Hook 复用。 */
function useCatalogCache() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const listKey = issueStatusKeys.list(wsId);

  /** 不可变地更新缓存里的状态数组：更新后重新排序，并同步 total。 */
  const writeStatuses = (
    update: (statuses: IssueStatusEntry[]) => IssueStatusEntry[] | null,
    totalDelta = 0,
  ) => {
    qc.setQueryData<ListIssueStatusesResponse>(listKey, (old) => {
      if (!old) return old;
      const statuses = update(old.statuses);
      if (!statuses) return old;
      return {
        ...old,
        statuses: statuses.sort(compareIssueStatusEntries),
        total: old.total + totalDelta,
      };
    });
  };

  return {
    qc,
    listKey,
    /**
     * 把一条新建的状态加进缓存目录。
     *
     * 当 id 已存在时跳过——这不仅是防重复：本客户端能已经持有该行的唯一途径，
     * 是一次把它读回来的重拉——可能还是别人编辑过之后的版本——所以缓存里的
     * 副本绝不会比这份响应里的旧。
     *
     * 要排序，因为 `position` 和 `category` 决定一行渲染在哪里，若是追加，
     * 一条新的 In Review 会掉到 Done 行的下面。
     */
    insertEntry: (entry: IssueStatusEntry) => {
      // 未能通过 schema 校验的响应会退化成空 stub（`parseWithFallback`）。
      // 把它写进缓存会在选择器里放一个空行；留着缓存不动，让实时重拉提供
      // 真相即可。
      if (!entry?.id) return;
      writeStatuses(
        (statuses) =>
          statuses.some((s) => s.id === entry.id) ? null : [...statuses, entry],
        1,
      );
    },
    /** 对缓存中的某一行打字段补丁，其余字段保持不动。 */
    patchEntry: (id: string, patch: Partial<IssueStatusEntry>) => {
      writeStatuses((statuses) =>
        statuses.some((s) => s.id === id)
          ? statuses.map((s) => (s.id === id ? { ...s, ...patch } : s))
          : null,
      );
    },
    invalidate: () => {
      qc.invalidateQueries({ queryKey: issueStatusKeys.all(wsId) });
    },
  };
}

/** 创建一条自定义状态，成功后插入目录缓存。 */
export function useCreateIssueStatus() {
  const { insertEntry, invalidate } = useCatalogCache();
  return useMutation({
    mutationFn: (data: CreateIssueStatusRequest) => api.createIssueStatus(data),
    onSuccess: insertEntry,
    onError: invalidate,
  });
}

/**
 * 乐观的改名 / 改色 / 重排——与 `useUpdateLabel` 同款结构。没有它，拖动一行
 * 重排会在网络往返期间弹回原位。
 */
export function useUpdateIssueStatus() {
  const { qc, listKey, patchEntry, invalidate } = useCatalogCache();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string } & UpdateIssueStatusRequest) =>
      api.updateIssueStatus(id, data),
    onMutate: async ({ id, ...data }) => {
      await qc.cancelQueries({ queryKey: listKey });
      const previous = qc.getQueryData<ListIssueStatusesResponse>(listKey);
      patchEntry(id, data);
      return { previous };
    },
    // 成功后什么都不做。乐观补丁已经展示了改动，实时重拉会把它定案；若在这里
    // 安装响应快照，恰恰可能把一次并发的编辑滚回去（见文件顶部说明）。
    //
    // 改名也刻意不去失效 issues 作用域。issue 行存的是状态 KEY；它的名称和
    // 颜色在渲染时从这份目录解析（`useStatusLabel`、`colorOf`），所以这里没有
    // 任何被缓存的 issue 字段会过期——刷新目录，看板自然就重绘了。旧的实现
    // 为了改一个字，会把整个工作区的看板、列表、表格全部重新拉一遍。(MUL-6458)
    onError: (_err, _vars, ctx) => {
      if (ctx?.previous) qc.setQueryData(listKey, ctx.previous);
      invalidate();
    },
  });
}

/**
 * 归档一条自定义状态。刻意不做乐观更新：服务端拒绝归档内置状态，若在拒绝
 * 到达前行就悄悄消失，会给人「成功」的错觉。
 */
export function useArchiveIssueStatus() {
  const { patchEntry, invalidate } = useCatalogCache();
  return useMutation({
    mutationFn: (id: string) => api.archiveIssueStatus(id),
    // 只写 `archived_at`，绝不写整行返回。归档是终局操作——没有取消归档，
    // 服务端也拒绝编辑归档状态——所以无论期间缓存里还落了什么，这个旗标都为
    // true；整行快照则会把与这次归档赛跑的某次改名一起回滚。
    //
    // 行本身保留：仍停在它上面的 issue 通过它解析名称、颜色和类别。
    // `activeStatuses` 负责把它从选择器里藏起来。
    onSuccess: (entry) => {
      if (!entry?.id) return;
      patchEntry(entry.id, { archived_at: entry.archived_at });
    },
    onError: invalidate,
  });
}

/**
 * 提交「单个类别内」的拖拽重排。
 *
 * 一次请求发完整顺序，而不是逐行 PATCH：一串写入不是原子的，中途某行被拒绝
 * （一个归档状态、一次并发归档）会留下「前面的行已经重排好了、调用方却被告知
 * 整件事失败」的局面。`ordered` 是该类别 ACTIVE 的自定义状态；服务端从 1 开始
 * 分配位置，因为该类别的内置状态固定在 0 且永不移动。
 */
export function useReorderIssueStatuses() {
  const { qc, listKey, invalidate } = useCatalogCache();
  return useMutation({
    mutationFn: ({
      category,
      ordered,
    }: {
      category: IssueStatusCategory;
      ordered: IssueStatusEntry[];
    }) => api.reorderIssueStatuses(category, ordered.map((entry) => entry.id)),
    onMutate: async ({ ordered }) => {
      await qc.cancelQueries({ queryKey: listKey });
      const previous = qc.getQueryData<ListIssueStatusesResponse>(listKey);
      const positionById = new Map(ordered.map((e, index) => [e.id, index + 1]));
      qc.setQueryData<ListIssueStatusesResponse>(listKey, (old) =>
        old
          ? {
              ...old,
              // 重排数组而非只改 position：消费方按数组顺序渲染，只写位置的话
              // 拖动在视觉上会一直「没生效」，直到重拉落地。
              statuses: old.statuses
                .map((s) =>
                  positionById.has(s.id) ? { ...s, position: positionById.get(s.id)! } : s,
                )
                .sort(compareIssueStatusEntries),
            }
          : old,
      );
      return { previous };
    },
    // 重排接口返回整份目录，而这正是不能安装响应体的原因：一张迟到的全表快照
    // 会覆盖每一行——一次慢的重排响应就会把重拉已经捡回来的所有并发编辑全部
    // 滚回去。
    onError: (_err, _vars, ctx) => {
      if (ctx?.previous) qc.setQueryData(listKey, ctx.previous);
      invalidate();
    },
  });
}
