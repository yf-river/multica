// 本文件定义工作区订阅（座位购买、对账、门户）的变更操作。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  CreateWorkspaceSubscriptionCheckoutRequest,
  PreviewWorkspaceSeatPurchaseRequest,
  PurchaseWorkspaceSeatsRequest,
} from "../types";
import { workspaceSubscriptionKeys } from "./workspace-subscription-queries";

/**
 * 创建工作区订阅 Checkout 会话的 mutation。成功后失效订阅摘要，让返回页面拿到新状态。
 */
export function useCreateWorkspaceSubscriptionCheckout(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateWorkspaceSubscriptionCheckoutRequest) =>
      api.createWorkspaceSubscriptionCheckout(data),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: workspaceSubscriptionKeys.summary(wsId),
      });
    },
  });
}

/**
 * 对账工作区订阅座位的 mutation。成功后失效订阅摘要。
 */
export function useReconcileWorkspaceSubscriptionSeats(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api.reconcileWorkspaceSubscriptionSeats(),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: workspaceSubscriptionKeys.summary(wsId),
      });
    },
  });
}

/**
 * 预览工作区座位购买（含价格估算）的 mutation，不做缓存失效。
 */
export function usePreviewWorkspaceSeatPurchase() {
  return useMutation({
    mutationFn: (data: PreviewWorkspaceSeatPurchaseRequest) =>
      api.previewWorkspaceSeatPurchase(data),
  });
}

/**
 * 购买工作区座位的 mutation。成功后失效订阅摘要。
 */
export function usePurchaseWorkspaceSeats(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: PurchaseWorkspaceSeatsRequest) =>
      api.purchaseWorkspaceSeats(data),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: workspaceSubscriptionKeys.summary(wsId),
      });
    },
  });
}

/**
 * 创建工作区订阅 Portal 会话的 mutation。成功后失效订阅摘要。
 */
export function useCreateWorkspaceSubscriptionPortal(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (idempotencyKey: string) =>
      api.createWorkspaceSubscriptionPortal(idempotencyKey),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: workspaceSubscriptionKeys.summary(wsId),
      });
    },
  });
}
