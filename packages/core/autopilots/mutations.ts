// 本文件定义自动驾驶（autopilot）相关的变更操作（mutation），封装对后端的增删改触发。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { autopilotKeys } from "./queries";
import { useWorkspaceId } from "../hooks";
import type {
  CreateAutopilotRequest,
  UpdateAutopilotRequest,
  ListAutopilotsResponse,
  GetAutopilotResponse,
  CreateAutopilotTriggerRequest,
  UpdateAutopilotTriggerRequest,
} from "../types";

/**
 * 创建自动驾驶的 mutation。成功后把新项插入列表缓存，并最终失效列表查询。
 */
export function useCreateAutopilot() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreateAutopilotRequest) => api.createAutopilot(data),
    onSuccess: (newAutopilot) => {
      qc.setQueryData<ListAutopilotsResponse>(autopilotKeys.list(wsId), (old) =>
        old && !old.autopilots.some((a) => a.id === newAutopilot.id)
          ? { ...old, autopilots: [...old.autopilots, newAutopilot], total: old.total + 1 }
          : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: autopilotKeys.list(wsId) });
    },
  });
}

/**
 * 更新自动驾驶的 mutation。采用乐观更新：先改列表与详情缓存，失败时回滚，
 * 最终失效详情和列表查询以拉取权威数据。
 */
export function useUpdateAutopilot() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string } & UpdateAutopilotRequest) =>
      api.updateAutopilot(id, data),
    onMutate: ({ id, ...data }) => {
      qc.cancelQueries({ queryKey: autopilotKeys.list(wsId) });
      const prevList = qc.getQueryData<ListAutopilotsResponse>(autopilotKeys.list(wsId));
      const prevDetail = qc.getQueryData<GetAutopilotResponse>(autopilotKeys.detail(wsId, id));
      // 请求形状（AutopilotSubscriberInput）缺少 `created_at`，因此不能赋给响应形状。
      // onSettled 会失效详情查询并重新拉取权威的服务端载荷。
      const { subscribers: _omitSubs, ...optimistic } = data;
      qc.setQueryData<ListAutopilotsResponse>(autopilotKeys.list(wsId), (old) =>
        old
          ? {
              ...old,
              autopilots: old.autopilots.map((a) =>
                a.id === id ? { ...a, ...optimistic } : a,
              ),
            }
          : old,
      );
      qc.setQueryData<GetAutopilotResponse>(autopilotKeys.detail(wsId, id), (old) =>
        old ? { ...old, autopilot: { ...old.autopilot, ...optimistic } } : old,
      );
      return { prevList, prevDetail, id };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prevList) qc.setQueryData(autopilotKeys.list(wsId), ctx.prevList);
      if (ctx?.prevDetail) qc.setQueryData(autopilotKeys.detail(wsId, ctx.id), ctx.prevDetail);
    },
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: autopilotKeys.detail(wsId, vars.id) });
      qc.invalidateQueries({ queryKey: autopilotKeys.list(wsId) });
    },
  });
}

/**
 * 删除自动驾驶的 mutation。乐观地从列表移除并删除详情缓存，失败时回滚。
 */
export function useDeleteAutopilot() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.deleteAutopilot(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: autopilotKeys.list(wsId) });
      const prevList = qc.getQueryData<ListAutopilotsResponse>(autopilotKeys.list(wsId));
      qc.setQueryData<ListAutopilotsResponse>(autopilotKeys.list(wsId), (old) =>
        old ? { ...old, autopilots: old.autopilots.filter((a) => a.id !== id), total: old.total - 1 } : old,
      );
      qc.removeQueries({ queryKey: autopilotKeys.detail(wsId, id) });
      return { prevList };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prevList) qc.setQueryData(autopilotKeys.list(wsId), ctx.prevList);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: autopilotKeys.list(wsId) });
    },
  });
}

/**
 * 手动触发一次自动驾驶的 mutation。结束后失效该自动驾驶的运行列表和详情。
 */
export function useTriggerAutopilot() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.triggerAutopilot(id),
    onSettled: (_data, _err, id) => {
      qc.invalidateQueries({ queryKey: autopilotKeys.runs(wsId, id) });
      qc.invalidateQueries({ queryKey: autopilotKeys.detail(wsId, id) });
    },
  });
}

/**
 * 授予某用户访问自动驾驶权限的 mutation。结束后失效该自动驾驶详情。
 */
export function useGrantAutopilotAccess() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ autopilotId, userId }: { autopilotId: string; userId: string }) =>
      api.grantAutopilotAccess(autopilotId, userId),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: autopilotKeys.detail(wsId, vars.autopilotId) });
    },
  });
}

/**
 * 撤销某用户访问自动驾驶权限的 mutation。结束后失效该自动驾驶详情。
 */
export function useRevokeAutopilotAccess() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ autopilotId, userId }: { autopilotId: string; userId: string }) =>
      api.revokeAutopilotAccess(autopilotId, userId),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: autopilotKeys.detail(wsId, vars.autopilotId) });
    },
  });
}

/**
 * 创建自动驾驶触发器的 mutation。结束后失效所属自动驾驶的详情。
 */
export function useCreateAutopilotTrigger() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ autopilotId, ...data }: { autopilotId: string } & CreateAutopilotTriggerRequest) =>
      api.createAutopilotTrigger(autopilotId, data),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: autopilotKeys.detail(wsId, vars.autopilotId) });
    },
  });
}

/**
 * 更新自动驾驶触发器的 mutation。结束后失效所属自动驾驶的详情。
 */
export function useUpdateAutopilotTrigger() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ autopilotId, triggerId, ...data }: { autopilotId: string; triggerId: string } & UpdateAutopilotTriggerRequest) =>
      api.updateAutopilotTrigger(autopilotId, triggerId, data),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: autopilotKeys.detail(wsId, vars.autopilotId) });
    },
  });
}

/**
 * 删除自动驾驶触发器的 mutation。结束后失效所属自动驾驶的详情。
 */
export function useDeleteAutopilotTrigger() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ autopilotId, triggerId }: { autopilotId: string; triggerId: string }) =>
      api.deleteAutopilotTrigger(autopilotId, triggerId),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: autopilotKeys.detail(wsId, vars.autopilotId) });
    },
  });
}

/**
 * 轮换自动驾驶 webhook 触发器 token 的 mutation。结束后失效所属自动驾驶的详情。
 */
export function useRotateAutopilotTriggerWebhookToken() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ autopilotId, triggerId }: { autopilotId: string; triggerId: string }) =>
      api.rotateAutopilotTriggerWebhookToken(autopilotId, triggerId),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: autopilotKeys.detail(wsId, vars.autopilotId) });
    },
  });
}

/**
 * 重放一次已记录的投递（delivery）。服务端会创建一条新的投递记录
 * （带 `replayed_from_delivery_id`）并同步启动一次新的自动驾驶运行。
 * 我们同时失效投递和运行两类查询，让新投递及可能产生的运行立即显现。
 */
export function useReplayAutopilotDelivery() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ autopilotId, deliveryId }: { autopilotId: string; deliveryId: string }) =>
      api.replayAutopilotDelivery(autopilotId, deliveryId),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: autopilotKeys.deliveries(wsId, vars.autopilotId) });
      qc.invalidateQueries({ queryKey: autopilotKeys.runs(wsId, vars.autopilotId) });
    },
  });
}
