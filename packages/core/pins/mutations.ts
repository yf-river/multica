// 本文件封装「固定项」的各类变更：创建、删除、拖动重排（带乐观更新）。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useAuthStore } from "../auth";
import { pinKeys } from "./queries";
import { useWorkspaceId } from "../hooks";
import type { PinnedItem, PinnedItemType } from "../types";

/** 新建一条固定项，成功后追加进本地缓存列表。 */
export function useCreatePin() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id ?? "");
  return useMutation({
    mutationFn: (data: { item_type: PinnedItemType; item_id: string }) =>
      api.createPin(data),
    onSuccess: (newPin) => {
      qc.setQueryData<PinnedItem[]>(pinKeys.list(wsId, userId), (old) =>
        old ? [...old, newPin] : [newPin],
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: pinKeys.list(wsId, userId) });
    },
  });
}

/** 删除一条固定项，乐观从本地缓存移除并支持回滚。 */
export function useDeletePin() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id ?? "");
  return useMutation({
    mutationFn: ({ itemType, itemId }: { itemType: PinnedItemType; itemId: string }) =>
      api.deletePin(itemType, itemId),
    onMutate: async ({ itemType, itemId }) => {
      await qc.cancelQueries({ queryKey: pinKeys.list(wsId, userId) });
      const prev = qc.getQueryData<PinnedItem[]>(pinKeys.list(wsId, userId));
      qc.setQueryData<PinnedItem[]>(pinKeys.list(wsId, userId), (old) =>
        old ? old.filter((p) => !(p.item_type === itemType && p.item_id === itemId)) : old,
      );
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(pinKeys.list(wsId, userId), ctx.prev);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: pinKeys.list(wsId, userId) });
    },
  });
}

/** 拖动重排固定项：按新顺序提交位置（1 起），本地乐观套用新数组。 */
export function useReorderPins() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id ?? "");
  return useMutation({
    mutationFn: (reorderedPins: PinnedItem[]) => {
      const items = reorderedPins.map((p, i) => ({ id: p.id, position: i + 1 }));
      return api.reorderPins({ items });
    },
    onMutate: async (reorderedPins) => {
      await qc.cancelQueries({ queryKey: pinKeys.list(wsId, userId) });
      const prev = qc.getQueryData<PinnedItem[]>(pinKeys.list(wsId, userId));
      qc.setQueryData<PinnedItem[]>(pinKeys.list(wsId, userId), reorderedPins);
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(pinKeys.list(wsId, userId), ctx.prev);
    },
  });
}
