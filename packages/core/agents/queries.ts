import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const agentTaskSnapshotKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-task-snapshot"] as const,
  list: (wsId: string) => [...agentTaskSnapshotKeys.all(wsId), "list"] as const,
};

export const agentActivityKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-activity"] as const,
  last30d: (wsId: string) => [...agentActivityKeys.all(wsId), "30d"] as const,
};

export const agentRunCountsKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-run-counts"] as const,
  last30d: (wsId: string) => [...agentRunCountsKeys.all(wsId), "30d"] as const,
};

export function agentTaskSnapshotOptions(wsId: string) {
  return queryOptions({
    queryKey: agentTaskSnapshotKeys.list(wsId),
    queryFn: () => api.getAgentTaskSnapshot(),
    staleTime: 30 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

export function agentActivity30dOptions(wsId: string) {
  return queryOptions({
    queryKey: agentActivityKeys.last30d(wsId),
    queryFn: () => api.getWorkspaceAgentActivity30d(),
    staleTime: 60 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

export function agentRunCounts30dOptions(wsId: string) {
  return queryOptions({
    queryKey: agentRunCountsKeys.last30d(wsId),
    queryFn: () => api.getWorkspaceAgentRunCounts(),
    staleTime: 60 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

export const agentTasksKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-tasks"] as const,
  detail: (wsId: string, agentId: string) =>
    [...agentTasksKeys.all(wsId), agentId] as const,
};

export function agentTasksOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: agentTasksKeys.detail(wsId, agentId),
    queryFn: () => api.listAgentTasks(agentId),
    staleTime: 30 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}
