import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { executeRecoverableMutation } from "../api/transport";
import { generateUUID } from "../utils";
import { autopilotKeys } from "./queries";
import { useWorkspaceId } from "../paths";
import type {
  CreateAutopilotRequest,
  UpdateAutopilotRequest,
  Autopilot,
  GetAutopilotResponse,
  CreateAutopilotTriggerRequest,
  UpdateAutopilotTriggerRequest,
} from "../types";
import { useAutopilotPendingOperationStore } from "./pending-operation-store";

export function useCreateAutopilot() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: async (data: CreateAutopilotRequest) => {
      const operations = useAutopilotPendingOperationStore.getState();
      const pendingCreate = operations.pendingCreate ?? {
        requestKey: generateUUID(),
        request: data,
      };
      operations.setPendingCreate(pendingCreate);
      return executeRecoverableMutation(
        () => api.createAutopilot(
          pendingCreate.request,
          pendingCreate.requestKey,
        ),
        () => useAutopilotPendingOperationStore.getState().setPendingCreate(),
      );
    },
    onSuccess: (newAutopilot) => {
      qc.setQueryData<Autopilot[]>(autopilotKeys.list(wsId), (old) =>
        old && !old.some((autopilot) => autopilot.id === newAutopilot.id)
          ? [...old, newAutopilot]
          : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: autopilotKeys.list(wsId) });
    },
  });
}

export function useUpdateAutopilot() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string } & UpdateAutopilotRequest) =>
      api.updateAutopilot(id, data),
    onMutate: ({ id, ...data }) => {
      qc.cancelQueries({ queryKey: autopilotKeys.list(wsId) });
      const prevList = qc.getQueryData<Autopilot[]>(autopilotKeys.list(wsId));
      const prevDetail = qc.getQueryData<GetAutopilotResponse>(autopilotKeys.detail(wsId, id));
      // Subscriber membership is refetched authoritatively on settle; keep
      // the optimistic list/detail patch limited to scalar fields.
      const { subscribers: _omitSubs, ...optimistic } = data;
      qc.setQueryData<Autopilot[]>(autopilotKeys.list(wsId), (old) =>
        old?.map((autopilot) =>
          autopilot.id === id ? { ...autopilot, ...optimistic } : autopilot,
        ),
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

export function useDeleteAutopilot() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.deleteAutopilot(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: autopilotKeys.list(wsId) });
      const prevList = qc.getQueryData<Autopilot[]>(autopilotKeys.list(wsId));
      qc.setQueryData<Autopilot[]>(autopilotKeys.list(wsId), (old) =>
        old?.filter((autopilot) => autopilot.id !== id),
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

export function useTriggerAutopilot() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: async (id: string) => {
      const operations = useAutopilotPendingOperationStore.getState();
      const requestKey = operations.manualTriggerKeys[id] ?? generateUUID();
      operations.setManualTriggerKey(id, requestKey);
      return executeRecoverableMutation(
        () => api.triggerAutopilot(id, requestKey),
        () => useAutopilotPendingOperationStore.getState().clearManualTriggerKey(id),
      );
    },
    onSettled: (_data, _err, id) => {
      qc.invalidateQueries({ queryKey: autopilotKeys.runs(wsId, id) });
      qc.invalidateQueries({ queryKey: autopilotKeys.detail(wsId, id) });
    },
  });
}

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

// Replay re-dispatches a previously-recorded delivery. The server creates
// a new delivery row (with `replayed_from_delivery_id`) and synchronously
// kicks off a new autopilot run. We invalidate both deliveries and runs so
// the new delivery and any resulting run show up immediately.
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
