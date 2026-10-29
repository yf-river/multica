import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const releaseAgentKeys = {
  settings: (workspaceId: string) => ["workspaces", workspaceId, "release-agent", "settings"] as const,
};

export function releaseAgentSettingsOptions(workspaceId: string, enabled = true) {
  return queryOptions({
    queryKey: releaseAgentKeys.settings(workspaceId),
    queryFn: () => api.getReleaseAgentSettings(),
    enabled,
    staleTime: 60 * 1000,
  });
}
