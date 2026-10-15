import {
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "./queries";
import type { WorkspaceRepo, CreateProjectResourceRequest } from "../types";

export {
  repositoryIdentity,
  repositoryProvider,
  repositoryStatus,
} from "./repository-identity";

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
  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: repositoryKeys.center(wsId) }),
      queryClient.invalidateQueries({ queryKey: workspaceKeys.list() }),
      queryClient.invalidateQueries({ queryKey: ["projects", wsId] }),
    ]);
  };
  const save = useMutation({
    mutationFn: async (repos: WorkspaceRepo[]) => {
      for (const repo of repos) await api.createRepository(wsId, repo);
    },
    onSettled: refresh,
  });
  const update = useMutation({
    mutationFn: ({ id, data }: { id: string; data: Partial<WorkspaceRepo> }) =>
      api.updateRepository(wsId, id, data),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteRepository(wsId, id),
    onSuccess: refresh,
  });
  const location = useMutation({
    mutationFn: ({
      id,
      ...data
    }: {
      id: string;
      daemon_id: string;
      local_path: string;
      execution_mode: "worktree" | "in_place";
    }) => api.putRepositoryLocation(wsId, id, data),
    onSuccess: refresh,
  });
  const removeLocation = useMutation({
    mutationFn: ({ id, locationId }: { id: string; locationId: string }) =>
      api.deleteRepositoryLocation(wsId, id, locationId),
    onSuccess: refresh,
  });
  const probe = useMutation({
    mutationFn: (url: string) => api.probeWorkspaceRepo(wsId, { url }),
  });
  const resolve = useMutation({
    mutationFn: (input: { url: string; default_branch?: string }) =>
      api.resolveWorkspaceRepo(wsId, input),
  });
  const connect = useMutation({
    mutationFn: () => api.getGitHubConnectURL(wsId, "repositories"),
  });
  const resource = useMutation({
    mutationFn: async (
      input:
        | { projectId: string; request: CreateProjectResourceRequest }
        | { projectId: string; resourceId: string },
    ) => {
      if ("request" in input)
        await api.createProjectResource(input.projectId, input.request);
      else await api.deleteProjectResource(input.projectId, input.resourceId);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: repositoryKeys.center(wsId),
      });
      await queryClient.invalidateQueries({ queryKey: ["projects", wsId] });
    },
  });
  return {
    save,
    update,
    remove,
    location,
    removeLocation,
    probe,
    resolve,
    connect,
    resource,
  };
}
