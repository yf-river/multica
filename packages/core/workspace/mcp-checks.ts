import {
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "./queries";
export function mcpCheckOptions(wsId: string, serverId: string) {
  return queryOptions({
    queryKey: [...workspaceKeys.mcpServers(wsId), serverId, "checks"],
    queryFn: () => api.workspaceMcpChecks(wsId, serverId),
    refetchInterval: (q) =>
      q.state.data?.some(
        (c) => c.status === "pending" || c.status === "running",
      )
        ? 1500
        : false,
  });
}
export function useMcpCheck(wsId: string, serverId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (runtimeId: string) =>
      api.workspaceMcpChecks(wsId, serverId, runtimeId),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: workspaceKeys.mcpServers(wsId) }),
  });
}
export function useMcpMetadata(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { serverId: string; name: string; description: string }) =>
      api.updateWorkspaceMcpMetadata(wsId, v.serverId, v.name, v.description),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: workspaceKeys.mcpServers(wsId) }),
  });
}
