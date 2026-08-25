import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../paths";
import { notificationPreferenceKeys } from "./queries";
import type { NotificationPreferences } from "../types";

export function useUpdateNotificationPreferences() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (preferences: NotificationPreferences) =>
      api.updateNotificationPreferences(preferences),
    onMutate: async (preferences) => {
      await qc.cancelQueries({ queryKey: notificationPreferenceKeys.all(wsId) });
      const prev = qc.getQueryData<NotificationPreferences>(
        notificationPreferenceKeys.all(wsId),
      );
      qc.setQueryData<NotificationPreferences>(
        notificationPreferenceKeys.all(wsId),
        preferences,
      );
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev) {
        qc.setQueryData(notificationPreferenceKeys.all(wsId), ctx.prev);
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: notificationPreferenceKeys.all(wsId) });
    },
  });
}
