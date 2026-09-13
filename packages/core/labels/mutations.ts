// 本文件封装「标签」的各类变更：创建/更新/删除标签本体，以及给 issue /
// agent / skill 挂标签、摘标签（带乐观更新与失败回滚）。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { labelKeys } from "./queries";
import { useWorkspaceId } from "../hooks";
import { issueKeys } from "../issues/queries";
import {
  invalidateIssueLabelDerivatives,
  onIssueLabelsChanged,
  patchIssueLabels,
} from "../issues/ws-updaters";
import type {
  Label,
  CreateLabelRequest,
  UpdateLabelRequest,
  ListLabelsResponse,
  IssueLabelsResponse,
  LabelResourceType,
  ResourceLabelsResponse,
} from "../types";

/** 创建一条标签，成功后写进缓存列表（按 id 去重）。 */
export function useCreateLabel() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreateLabelRequest) => api.createLabel(data),
    onSuccess: (label) => {
      qc.setQueryData<ListLabelsResponse>(labelKeys.list(wsId, label.resource_type ?? "issue"), (old) =>
        old && !old.labels.some((l) => l.id === label.id)
          ? { ...old, labels: [...old.labels, label], total: old.total + 1 }
          : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: labelKeys.all(wsId) });
    },
  });
}

/**
 * 乐观的改名 / 改色。与 useUpdateProject 同款模式：本地应用改动、快照供回滚、
 * settle 时失效。没有它，每次编辑 UI 都会在网络往返期间卡住。
 */
export function useUpdateLabel() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, resource_type: _resourceType, ...data }: { id: string; resource_type?: LabelResourceType } & UpdateLabelRequest) =>
      api.updateLabel(id, data),
    onMutate: async ({ id, resource_type = "issue", ...data }) => {
      const listKey = labelKeys.list(wsId, resource_type);
      await qc.cancelQueries({ queryKey: listKey });
      const prevList = qc.getQueryData<ListLabelsResponse>(listKey);
      qc.setQueryData<ListLabelsResponse>(listKey, (old) =>
        old
          ? {
              ...old,
              labels: old.labels.map((l) => (l.id === id ? { ...l, ...data } : l)),
            }
          : old,
      );
      return { prevList, listKey };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prevList && ctx.listKey) qc.setQueryData(ctx.listKey, ctx.prevList);
    },
    onSettled: () => {
      // 失效整个 labels 作用域，让任何带着这份标签旧副本的 byIssue 缓存都
      // 重新拉取。列表缓存是唯一真源；byIssue 视图会用新数据重新渲染。
      qc.invalidateQueries({ queryKey: labelKeys.all(wsId) });
      // issue 现在内嵌标签（反规范化快照），所以改名/改色也要刷新持有这些
      // 快照的 issues 缓存。
      qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
    },
  });
}

/** 删除一条标签（可传字符串 id 或带资源类型的对象），乐观从列表缓存移除。 */
export function useDeleteLabel() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (input: string | { id: string; resource_type: LabelResourceType }) =>
      api.deleteLabel(typeof input === "string" ? input : input.id),
    onMutate: async (input) => {
      const id = typeof input === "string" ? input : input.id;
      const resourceType = typeof input === "string" ? "issue" : input.resource_type;
      const listKey = labelKeys.list(wsId, resourceType);
      await qc.cancelQueries({ queryKey: listKey });
      const prev = qc.getQueryData<ListLabelsResponse>(listKey);
      qc.setQueryData<ListLabelsResponse>(listKey, (old) =>
        old
          ? { ...old, labels: old.labels.filter((l) => l.id !== id), total: old.total - 1 }
          : old,
      );
      return { prev, listKey };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev && ctx.listKey) qc.setQueryData(ctx.listKey, ctx.prev);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: labelKeys.all(wsId) });
      // 被删的标签在重新拉取前仍留在缓存的 issue.labels 数组里——失效 issues
      // 缓存，让列表/看板上的 chip 丢掉这个孤儿。
      qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
    },
  });
}

/** 给某个 agent / skill 资源挂一条标签，成功后直接写入该资源的标签缓存。 */
export function useAttachResourceLabel(
  resourceType: "agent" | "skill",
  resourceId: string,
) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (labelId: string) =>
      api.attachLabelToResource(resourceType, resourceId, labelId),
    onSuccess: (data: ResourceLabelsResponse) => {
      qc.setQueryData(
        labelKeys.byResource(wsId, resourceType, resourceId),
        data,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: labelKeys.all(wsId) });
      qc.invalidateQueries({ queryKey: workspaceKeysForLabels(resourceType, wsId) });
    },
  });
}

/** 从某个 agent / skill 资源上摘掉一条标签，成功后直接写入该资源的标签缓存。 */
export function useDetachResourceLabel(
  resourceType: "agent" | "skill",
  resourceId: string,
) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (labelId: string) =>
      api.detachLabelFromResource(resourceType, resourceId, labelId),
    onSuccess: (data: ResourceLabelsResponse) => {
      qc.setQueryData(
        labelKeys.byResource(wsId, resourceType, resourceId),
        data,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: labelKeys.all(wsId) });
      qc.invalidateQueries({ queryKey: workspaceKeysForLabels(resourceType, wsId) });
    },
  });
}

/** 拼出某资源类型对应的「工作区详情」查询 key（labels 缓存外还需刷新的地方）。 */
function workspaceKeysForLabels(resourceType: "agent" | "skill", wsId: string) {
  return ["workspaces", wsId, resourceType === "agent" ? "agents" : "skills"] as const;
}

/** 挂/摘标签前，把会受影响的各类 issues 查询全部取消在途。 */
async function cancelIssueLabelMutationQueries(
  qc: ReturnType<typeof useQueryClient>,
  wsId: string,
  issueId: string,
) {
  await Promise.all([
    qc.cancelQueries({ queryKey: labelKeys.byIssue(wsId, issueId) }),
    qc.cancelQueries({ queryKey: issueKeys.list(wsId) }),
    qc.cancelQueries({ queryKey: issueKeys.flatAll(wsId) }),
    qc.cancelQueries({ queryKey: issueKeys.childrenAll(wsId) }),
    qc.cancelQueries({ queryKey: issueKeys.childrenByParentsAll(wsId) }),
  ]);
}

/** 给某个 issue 挂标签（钩子绑定单个 issueId 的乐观流程）。 */
export function useAttachLabel(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (labelId: string) => api.attachLabel(issueId, labelId),
    onMutate: async (labelId) => {
      await cancelIssueLabelMutationQueries(qc, wsId, issueId);
      const prev = qc.getQueryData<IssueLabelsResponse>(labelKeys.byIssue(wsId, issueId));
      // 只有已经知道当前标签集时才打补丁——否则往空数组上追加 `[label]`
      // 会抹掉 issue 列表/详情缓存里反规范化的标签，回滚也救不回来。若
      // byIssue 还没缓存（用户在 picker 拉取前就点了），跳过乐观补丁，靠
      // onSettled 的重拉兜底。
      if (!prev) return { prev };
      if (prev.labels.some((l) => l.id === labelId)) return { prev };
      const list = qc.getQueryData<ListLabelsResponse>(labelKeys.list(wsId));
      const label = list?.labels.find((l) => l.id === labelId);
      if (!label) return { prev };
      const next: IssueLabelsResponse = { ...prev, labels: [...prev.labels, label] };
      qc.setQueryData<IssueLabelsResponse>(labelKeys.byIssue(wsId, issueId), next);
      patchIssueLabels(qc, wsId, issueId, next.labels);
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) {
        qc.setQueryData(labelKeys.byIssue(wsId, issueId), ctx.prev);
        patchIssueLabels(qc, wsId, issueId, ctx.prev.labels);
      }
    },
    onSuccess: (data: IssueLabelsResponse) => {
      // 变更后的读取失败时后端可能返回空对象（它记一条告警并跳过广播）。
      // 只有后端真的给了列表才应用——否则 onMutate 的乐观补丁一直撑到
      // onSettled 的失效重拉。
      if (data && Array.isArray(data.labels)) {
        onIssueLabelsChanged(qc, wsId, issueId, data.labels, data.issue_revision);
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });
      invalidateIssueLabelDerivatives(qc, wsId);
    },
  });
}

/**
 * 给「由变更参数而不是闭包中的 issueId」标识的 issue 挂标签。
 *
 * `useAttachLabel` 在调用 hook 时绑定单个 issueId，服务于乐观的 issue 详情
 * 流程；这个变体把 id 推迟到调用时才给，让调用方可以在渲染时还不存在的
 * issue 上挂标签——例如创建 issue 对话框里选好的标签，在 issue 创建完成后
 * 立刻挂上去。不做乐观补丁：创建流程会关掉对话框，靠 settle 时的失效来呈现
 * 新标签。
 */
export function useAttachLabelToIssue() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ issueId, labelId }: { issueId: string; labelId: string }) =>
      api.attachLabel(issueId, labelId),
    onSettled: (_data, _err, { issueId }) => {
      qc.invalidateQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });
      // issue 内嵌反规范化的标签快照，所以挂标签落定后要刷新持有它的 issues
      // 缓存（列表 / 看板 / 详情）。
      qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
    },
  });
}

/** 从某个 issue 上摘掉一条标签（钩子绑定单个 issueId 的乐观流程）。 */
export function useDetachLabel(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (labelId: string) => api.detachLabel(issueId, labelId),
    onMutate: async (labelId) => {
      await cancelIssueLabelMutationQueries(qc, wsId, issueId);
      const prev = qc.getQueryData<IssueLabelsResponse>(labelKeys.byIssue(wsId, issueId));
      const next = prev
        ? { ...prev, labels: prev.labels.filter((l: Label) => l.id !== labelId) }
        : undefined;
      if (next) {
        qc.setQueryData<IssueLabelsResponse>(labelKeys.byIssue(wsId, issueId), next);
        patchIssueLabels(qc, wsId, issueId, next.labels);
      }
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) {
        qc.setQueryData(labelKeys.byIssue(wsId, issueId), ctx.prev);
        patchIssueLabels(qc, wsId, issueId, ctx.prev.labels);
      }
    },
    onSuccess: (data: IssueLabelsResponse) => {
      if (data && Array.isArray(data.labels)) {
        onIssueLabelsChanged(qc, wsId, issueId, data.labels, data.issue_revision);
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });
      invalidateIssueLabelDerivatives(qc, wsId);
    },
  });
}
