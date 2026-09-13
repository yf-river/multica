// 本文件封装「自定义属性」的变更：创建/更新属性定义，以及给某个 issue 设置/
// 取消某个属性的值（后者带乐观更新与并发安全处理）。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { propertyKeys } from "./queries";
import { useWorkspaceId } from "../hooks";
import { issueKeys } from "../issues/queries";
import {
  invalidatePropertyWindowQueries,
  onIssuePropertiesChanged,
  patchIssueProperties,
} from "../issues/ws-updaters";
import { findIssueLocation } from "../issues/cache-helpers";
import type { IssueFlatCache } from "../issues/cache-coordinator";
import type {
  CreatePropertyRequest,
  UpdatePropertyRequest,
  Issue,
  IssuePropertyValue,
  IssuePropertyValues,
  ListIssuesCache,
} from "../types";

/** 创建一条属性定义，成功后失效属性目录缓存。 */
export function useCreateProperty() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreatePropertyRequest) => api.createProperty(data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: propertyKeys.all(wsId) });
    },
  });
}

/**
 * 属性定义的更新（改名、选项、归档）。不做乐观补丁：编辑发生在设置对话框里，
 * 一次往返可接受，而且配置的规范化（选项 id 分配）反正归服务端。
 */
export function useUpdateProperty() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string } & UpdatePropertyRequest) =>
      api.updateProperty(id, data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: propertyKeys.all(wsId) });
      // issue 缓存内嵌属性值包；定义变化（改名、选项编辑）会改变值的渲染，
      // 而引用已归档定义的行也必须从「+ 添加属性」菜单里消失。
      qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
    },
  });
}

/**
 * 从任一份持有该 issue 的缓存里读它的属性值包：先看详情缓存，再看各列表缓存
 * 桶。看板/列表界面从不填充详情缓存，只停在详情缓存的话，会让下面的乐观合并
 * 用单个 key 覆盖整个值包。
 */
function readIssueProperties(qc: ReturnType<typeof useQueryClient>, wsId: string, issueId: string): IssuePropertyValues | undefined {
  const detail = qc.getQueryData<Issue>(issueKeys.detail(wsId, issueId));
  if (detail) return detail.properties ?? {};
  for (const [, data] of qc.getQueriesData<ListIssuesCache>({ queryKey: issueKeys.list(wsId) })) {
    if (!data) continue;
    const location = findIssueLocation(data, issueId);
    if (location) return location.issue.properties ?? {};
  }
  for (const [, data] of qc.getQueriesData<IssueFlatCache>({
    queryKey: issueKeys.flatAll(wsId),
  })) {
    for (const page of data?.pages ?? []) {
      const issue = page.issues.find((candidate) => candidate.id === issueId);
      if (issue) return issue.properties ?? {};
    }
  }
  for (const [, data] of qc.getQueriesData<Issue[]>({
    queryKey: issueKeys.childrenAll(wsId),
  })) {
    const issue = data?.find((candidate) => candidate.id === issueId);
    if (issue) return issue.properties ?? {};
  }
  return undefined;
}

/** 属性变更前，把会受影响的各类 issues 查询全部取消在途。 */
async function cancelIssuePropertyMutationQueries(
  qc: ReturnType<typeof useQueryClient>,
  wsId: string,
  issueId: string,
) {
  await Promise.all([
    qc.cancelQueries({ queryKey: issueKeys.detail(wsId, issueId) }),
    qc.cancelQueries({ queryKey: issueKeys.list(wsId) }),
    qc.cancelQueries({ queryKey: issueKeys.flatAll(wsId) }),
    qc.cancelQueries({ queryKey: issueKeys.childrenAll(wsId) }),
    qc.cancelQueries({ queryKey: issueKeys.childrenByParentsAll(wsId) }),
  ]);
}

/**
 * 给某个 issue 乐观地写入单个属性。
 *
 * 并发契约（MUL-4463 评审第 1/2 轮）：
 *   - 同一 issue 的变更通过 TanStack mutation 的 `scope` 串行化，因此「全量
 *     值包」的响应不会乱序落地，快速的多选开关不会互相穿插。
 *   - 乐观补丁是「合并」进从任一缓存（详情或列表）读到的值包——绝不整体替换
 *     ——因此只显示看板的界面也能保住该 issue 的其他属性值。
 *   - 失败恢复快照；若原本没有快照，则回退为失效。
 *   - 该 issue 最后一次落定的变更会权威地失效详情缓存与定义目录（用量计数），
 *     在 staleTime:Infinity 下对齐任何赛跑的 WS 快照。
 */
export function useSetIssueProperty() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ issueId, propertyId, value }: { issueId: string; propertyId: string; value: IssuePropertyValue }) =>
      api.setIssueProperty(issueId, propertyId, value),
    scope: { id: `issue-properties:${wsId}` },
    mutationKey: ["issue-properties", wsId],
    onMutate: async ({ issueId, propertyId, value }) => {
      // 本次写入之前快照的响应，绝不能落在乐观补丁之后，把反规范化的 issue
      // 投影回滚掉。
      await cancelIssuePropertyMutationQueries(qc, wsId, issueId);
      const prev = readIssueProperties(qc, wsId, issueId);
      patchIssueProperties(qc, wsId, issueId, { ...(prev ?? {}), [propertyId]: value });
      return { prevValue: prev?.[propertyId], hadBag: prev !== undefined, issueId, propertyId };
    },
    onError: (_err, _vars, ctx) => {
      if (!ctx) return;
      rollbackSingleKey(qc, wsId, ctx);
    },
    onSuccess: (data, { issueId }) => {
      onIssuePropertiesChanged(qc, wsId, issueId, data.properties ?? {}, data.issue_revision);
    },
    onSettled: (_data, _err, { issueId }) => {
      settleIssuePropertyCaches(qc, wsId, issueId);
    },
  });
}

/** 取消某个 issue 上的某个属性值（乐观移除该 key）。 */
export function useUnsetIssueProperty() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ issueId, propertyId }: { issueId: string; propertyId: string }) =>
      api.unsetIssueProperty(issueId, propertyId),
    scope: { id: `issue-properties:${wsId}` },
    mutationKey: ["issue-properties", wsId],
    onMutate: async ({ issueId, propertyId }) => {
      await cancelIssuePropertyMutationQueries(qc, wsId, issueId);
      const prev = readIssueProperties(qc, wsId, issueId);
      if (prev) {
        const next = { ...prev };
        delete next[propertyId];
        patchIssueProperties(qc, wsId, issueId, next);
      }
      return { prevValue: prev?.[propertyId], hadBag: prev !== undefined, issueId, propertyId };
    },
    onError: (_err, _vars, ctx) => {
      if (!ctx) return;
      rollbackSingleKey(qc, wsId, ctx);
    },
    onSuccess: (data, { issueId }) => {
      onIssuePropertiesChanged(qc, wsId, issueId, data.properties ?? {}, data.issue_revision);
    },
    onSettled: (_data, _err, { issueId }) => {
      settleIssuePropertyCaches(qc, wsId, issueId);
    },
  });
}

/**
 * 只回滚本次变更动过的那一个 key，基于「当前」值包。
 *
 * 恢复整个 onMutate 快照会抹掉请求在途期间、别的 key 经 WS 落地的并发远端
 * 写入（净室评审 F2 交错 B）。
 */
function rollbackSingleKey(
  qc: ReturnType<typeof useQueryClient>,
  wsId: string,
  ctx: { prevValue: IssuePropertyValue | undefined; hadBag: boolean; issueId: string; propertyId: string },
) {
  if (!ctx.hadBag) {
    qc.invalidateQueries({ queryKey: issueKeys.detail(wsId, ctx.issueId) });
    return;
  }
  const current = readIssueProperties(qc, wsId, ctx.issueId) ?? {};
  const next = { ...current };
  if (ctx.prevValue === undefined) delete next[ctx.propertyId];
  else next[ctx.propertyId] = ctx.prevValue;
  patchIssueProperties(qc, wsId, ctx.issueId, next);
}

/** 最后一个在途的属性写入落定时，做一次权威对账。 */
function settleIssuePropertyCaches(qc: ReturnType<typeof useQueryClient>, wsId: string, issueId: string) {
  if (qc.isMutating({ mutationKey: ["issue-properties", wsId] }) > 1) return;
  qc.invalidateQueries({ queryKey: issueKeys.detail(wsId, issueId) });
  qc.invalidateQueries({ queryKey: propertyKeys.all(wsId) });
  // 服务端过滤/排序的窗口必须重新拉取：写入可能让这个 issue 跨页移动或重排
  // （onIssuePropertiesChanged 已为 WS 路径触发这个；本地变更需要在 settle 时
  // 补上，因为自产 WS 事件可能被抑制或赛跑）。
  invalidatePropertyWindowQueries(qc, wsId);
}
