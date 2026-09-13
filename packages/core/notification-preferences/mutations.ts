// 本文件封装「更新通知偏好」的 React Query 变更，并把同一工作区的偏好写入
// 串行化，避免慢的旧请求覆盖用户最新的开关切换。

import {
  useMutation,
  useQueryClient,
  type MutateOptions,
} from "@tanstack/react-query";
import { useCallback, useRef } from "react";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { useRequiredWorkspaceSlug } from "../paths";
import { notificationPreferenceKeys } from "./queries";
import type {
  NotificationPreferences,
  NotificationPreferenceResponse,
} from "../types";
import {
  applyNotificationPreferencePatch,
  deriveNotificationPreferencePatch,
  rollbackNotificationPreferencePatch,
} from "./patch";

/** 变更内部使用的变量：完整偏好 + 已推导出的补丁 + 工作区定位信息。 */
interface NotificationPreferenceMutationVariables {
  preferences: NotificationPreferences;
  patch: NotificationPreferences;
  workspaceId: string;
  workspaceSlug: string;
}

/** 乐观更新的上下文：回滚所需的旧值快照与 query key。 */
interface NotificationPreferenceMutationContext {
  previous: NotificationPreferenceResponse | undefined;
  key: readonly unknown[];
}

/** 外部调用方看到的 mutation options 类型（变量是「完整偏好」）。 */
type ExternalMutationOptions = MutateOptions<
  NotificationPreferenceResponse,
  Error,
  NotificationPreferences,
  NotificationPreferenceMutationContext
>;

/** 内部 mutation 的 options 类型（变量是带补丁的内部结构）。 */
type InternalMutationOptions = MutateOptions<
  NotificationPreferenceResponse,
  Error,
  NotificationPreferenceMutationVariables,
  NotificationPreferenceMutationContext
>;

/** 把外部 options 里的回调，映射成接收内部变量的形式。 */
function mapMutationOptions(
  preferences: NotificationPreferences,
  options: ExternalMutationOptions | undefined,
): InternalMutationOptions | undefined {
  if (!options) return undefined;

  return {
    onSuccess: options.onSuccess
      ? (data, _variables, result, context) =>
          options.onSuccess?.(data, preferences, result, context)
      : undefined,
    onError: options.onError
      ? (error, _variables, result, context) =>
          options.onError?.(error, preferences, result, context)
      : undefined,
    onSettled: options.onSettled
      ? (data, error, _variables, result, context) =>
          options.onSettled?.(data, error, preferences, result, context)
      : undefined,
  };
}

/**
 * 更新通知偏好。
 *
 * 把工作区偏好写入串行化：较慢的早先 PATCH 不会覆盖用户最新的开关切换。
 * 变量里同时带着工作区 slug，因为排队中的 mutation 可能在跳转之后才执行。
 *
 * @returns mutation 对象（mutate / mutateAsync 接受「完整偏好」而不是补丁）
 */
export function useUpdateNotificationPreferences() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const workspaceSlug = useRequiredWorkspaceSlug();
  const key = notificationPreferenceKeys.all(wsId);
  const renderedPreferences =
    qc.getQueryData<NotificationPreferenceResponse>(key)?.preferences ?? {};
  const renderedPreferencesRef = useRef(renderedPreferences);
  renderedPreferencesRef.current = renderedPreferences;

  // 把工作区偏好写入串行化，避免慢的早先 PATCH 覆盖用户最新开关；变量同时
  // 捕获 slug，因为排队的 mutation 可能在导航之后才执行。
  const mutation = useMutation<
    NotificationPreferenceResponse,
    Error,
    NotificationPreferenceMutationVariables,
    NotificationPreferenceMutationContext
  >({
    mutationKey: key,
    scope: { id: `notification-preferences:${wsId}` },
    mutationFn: ({ patch, workspaceSlug: targetWorkspaceSlug }) =>
      api.updateNotificationPreferences(patch, targetWorkspaceSlug),
    onMutate: async ({ patch, workspaceId }) => {
      const targetKey = notificationPreferenceKeys.all(workspaceId);
      await qc.cancelQueries({ queryKey: targetKey });
      const previous =
        qc.getQueryData<NotificationPreferenceResponse>(targetKey);
      qc.setQueryData<NotificationPreferenceResponse>(targetKey, (old) => ({
        ...(old ?? { workspace_id: workspaceId }),
        preferences: applyNotificationPreferencePatch(
          old?.preferences ?? {},
          patch,
        ),
      }));
      return { previous, key: targetKey };
    },
    onError: (_error, { patch, workspaceId }, context) => {
      const targetKey =
        context?.key ?? notificationPreferenceKeys.all(workspaceId);
      qc.setQueryData<NotificationPreferenceResponse>(targetKey, (old) => ({
        ...(old ?? { workspace_id: workspaceId }),
        preferences: rollbackNotificationPreferencePatch(
          old?.preferences ?? {},
          patch,
          context?.previous?.preferences ?? {},
        ),
      }));
    },
    onSettled: (_data, _error, { workspaceId }, context) => {
      const mutationKey = notificationPreferenceKeys.all(workspaceId);
      // onSettled 期间当前 mutation 仍被计入在途数。要等该工作区所有排队的
      // 乐观补丁都结束后再做对账。
      if (qc.isMutating({ mutationKey }) > 1) return;
      qc.invalidateQueries({ queryKey: context?.key ?? mutationKey });
    },
  });

  const mutate = useCallback(
    (
      preferences: NotificationPreferences,
      options?: ExternalMutationOptions,
    ) => {
      const patch = deriveNotificationPreferencePatch(
        renderedPreferencesRef.current,
        preferences,
      );
      mutation.mutate(
        { preferences, patch, workspaceId: wsId, workspaceSlug },
        mapMutationOptions(preferences, options),
      );
    },
    [mutation, workspaceSlug, wsId],
  );

  const mutateAsync = useCallback(
    (
      preferences: NotificationPreferences,
      options?: ExternalMutationOptions,
    ) => {
      const patch = deriveNotificationPreferencePatch(
        renderedPreferencesRef.current,
        preferences,
      );
      return mutation.mutateAsync(
        { preferences, patch, workspaceId: wsId, workspaceSlug },
        mapMutationOptions(preferences, options),
      );
    },
    [mutation, workspaceSlug, wsId],
  );

  return {
    ...mutation,
    variables: mutation.variables?.preferences,
    mutate,
    mutateAsync,
  };
}
