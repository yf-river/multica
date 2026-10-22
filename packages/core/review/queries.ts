import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const reviewKeys = {
  all: (wsId: string) => ["workspaces", wsId, "review"] as const,
  settings: (wsId: string) => [...reviewKeys.all(wsId), "settings"] as const,
};

/** 审查设置只在打开面板时读取，配置很少变化，不做轮询。 */
export function reviewSettingsOptions(wsId: string, enabled = true) {
  return queryOptions({
    queryKey: reviewKeys.settings(wsId),
    queryFn: () => api.getReviewSettings(),
    enabled,
    staleTime: 60 * 1000,
  });
}
