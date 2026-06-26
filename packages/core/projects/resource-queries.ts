import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { projectKeys } from "./queries";
import type {
  CreateProjectResourceRequest,
  ListProjectResourcesResponse,
  ProjectResource,
  UpdateProjectResourceRequest,
} from "../types";

export const projectResourceKeys = {
  list: (wsId: string, projectId: string) =>
    [...projectKeys.detail(wsId, projectId), "resources"] as const,
};

export function projectResourcesOptions(wsId: string, projectId: string) {
  return queryOptions({
    queryKey: projectResourceKeys.list(wsId, projectId),
    queryFn: () => api.listProjectResources(projectId),
    select: (data) => data.resources,
  });
}

export function useCreateProjectResource(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateProjectResourceRequest) =>
      api.createProjectResource(projectId, data),
    onSuccess: (created) => {
      qc.setQueryData<ListProjectResourcesResponse>(
        projectResourceKeys.list(wsId, projectId),
        (old) =>
          old && !old.resources.some((r) => r.id === created.id)
            ? {
                ...old,
                resources: [...old.resources, created],
                total: old.total + 1,
              }
            : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: projectResourceKeys.list(wsId, projectId),
      });
    },
  });
}

export function useUpdateProjectResource(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      resourceId,
      data,
    }: {
      resourceId: string;
      data: UpdateProjectResourceRequest;
    }) => api.updateProjectResource(projectId, resourceId, data),
    onSuccess: (updated) => {
      qc.setQueryData<ListProjectResourcesResponse>(
        projectResourceKeys.list(wsId, projectId),
        (old) =>
          old
            ? {
                ...old,
                resources: old.resources.map((r) =>
                  r.id === updated.id ? updated : r,
                ),
              }
            : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: projectResourceKeys.list(wsId, projectId),
      });
    },
  });
}

function replaceProjectResource(
  old: ListProjectResourcesResponse | undefined,
  updated: ProjectResource,
): ListProjectResourcesResponse | undefined {
  return old
    ? {
        ...old,
        resources: old.resources.map((r) =>
          r.id === updated.id ? updated : r,
        ),
      }
    : old;
}

function useProjectResourceAction(
  wsId: string,
  projectId: string,
  action: "test" | "sync" | "disable",
) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (resourceId: string) => {
      if (action === "test") return api.testProjectResource(projectId, resourceId);
      if (action === "sync") return api.syncProjectResource(projectId, resourceId);
      return api.disableProjectResource(projectId, resourceId);
    },
    onSuccess: (updated) => {
      qc.setQueryData<ListProjectResourcesResponse>(
        projectResourceKeys.list(wsId, projectId),
        (old) => replaceProjectResource(old, updated),
      );
    },
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: projectResourceKeys.list(wsId, projectId),
      });
    },
  });
}

export function useTestProjectResource(wsId: string, projectId: string) {
  return useProjectResourceAction(wsId, projectId, "test");
}

export function useSyncProjectResource(wsId: string, projectId: string) {
  return useProjectResourceAction(wsId, projectId, "sync");
}

export function useDisableProjectResource(wsId: string, projectId: string) {
  return useProjectResourceAction(wsId, projectId, "disable");
}

export function useEnableProjectResource(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (resourceId: string) =>
      api.enableProjectResource(projectId, resourceId),
    onSuccess: (updated) => {
      qc.setQueryData<ListProjectResourcesResponse>(
        projectResourceKeys.list(wsId, projectId),
        (old) => replaceProjectResource(old, updated),
      );
    },
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: projectResourceKeys.list(wsId, projectId),
      });
    },
  });
}

export function useDeleteProjectResource(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (resourceId: string) =>
      api.deleteProjectResource(projectId, resourceId),
    onMutate: async (resourceId) => {
      await qc.cancelQueries({
        queryKey: projectResourceKeys.list(wsId, projectId),
      });
      const prev = qc.getQueryData<ListProjectResourcesResponse>(
        projectResourceKeys.list(wsId, projectId),
      );
      qc.setQueryData<ListProjectResourcesResponse>(
        projectResourceKeys.list(wsId, projectId),
        (old) =>
          old
            ? {
                ...old,
                resources: old.resources.filter(
                  (r: ProjectResource) => r.id !== resourceId,
                ),
                total: old.total - 1,
              }
            : old,
      );
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) {
        qc.setQueryData(projectResourceKeys.list(wsId, projectId), ctx.prev);
      }
    },
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: projectResourceKeys.list(wsId, projectId),
      });
    },
  });
}
