import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "./queries";
import type { Workspace, WorkspaceRepo, CreateProjectResourceRequest } from "../types";
import type { WorkspaceRepositoryCenter } from "../types/workspace";

export { repositoryIdentity, repositoryProvider, repositoryStatus } from "./repository-identity";

export const repositoryKeys = {
  center: (wsId: string) => ["workspace-repositories", wsId] as const,
};

export function repositoryCenterOptions(wsId: string) {
  return queryOptions({
    queryKey: repositoryKeys.center(wsId),
    queryFn: async () => {
      const result = await api.getWorkspaceRepositoryCenter(wsId);
      if (!result) throw new Error("仓库详情响应格式异常，请重新加载");
      return result;
    },
  });
}

/** 仓库管理与项目资源使用同一 API 和查询缓存。 */
export function useRepositoryActions(wsId: string) {
  const queryClient = useQueryClient();
  const save = useMutation({
    mutationFn: (repos: WorkspaceRepo[]) => api.updateWorkspace(wsId, { repos }),
    onSuccess: (updated) => {
      queryClient.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((workspace) => workspace.id === updated.id ? updated : workspace),
      );
      queryClient.setQueryData(repositoryKeys.center(wsId), (old: WorkspaceRepositoryCenter | undefined) =>
        old ? { ...old, repos: updated.repos } : old,
      );
      void queryClient.invalidateQueries({ queryKey: repositoryKeys.center(wsId) });
    },
  });
  const probe = useMutation({
    mutationFn: (url: string) => api.probeWorkspaceRepo(wsId, { url }),
  });
  const resolve = useMutation({
    mutationFn: (input: { url: string; default_branch?: string }) => api.resolveWorkspaceRepo(wsId, input),
  });
  const connect = useMutation({
    mutationFn: () => api.getGitHubConnectURL(wsId, "repositories"),
  });
  const resource = useMutation({
    mutationFn: async (input: { projectId: string; request: CreateProjectResourceRequest } | { projectId: string; resourceId: string }) => {
      if ("request" in input) await api.createProjectResource(input.projectId, input.request);
      else await api.deleteProjectResource(input.projectId, input.resourceId);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: repositoryKeys.center(wsId) });
      await queryClient.invalidateQueries({ queryKey: ["projects", wsId] });
    },
  });
  return { save, probe, resolve, connect, resource };
}
