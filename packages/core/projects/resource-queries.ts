import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { projectKeys } from "./queries";
import type {
  CreateProjectResourceRequest,
  ProjectResource,
} from "../types";

export const projectResourceKeys = {
  list: (wsId: string, projectId: string) =>
    [...projectKeys.detail(wsId, projectId), "resources"] as const,
};

export function projectResourcesOptions(wsId: string, projectId: string) {
  return queryOptions({
    queryKey: projectResourceKeys.list(wsId, projectId),
    queryFn: () => api.listProjectResources(projectId),
  });
}

export function useCreateProjectResource(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateProjectResourceRequest) =>
      api.createProjectResource(projectId, data),
    onSuccess: (created) => {
      qc.setQueryData<ProjectResource[]>(
        projectResourceKeys.list(wsId, projectId),
        (old) =>
          old && !old.some((r) => r.id === created.id)
            ? [...old, created]
            : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: projectKeys.all(wsId),
      });
    },
  });
}

export function useSyncProjectResource(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (resourceId: string) =>
      api.syncProjectResource(projectId, resourceId),
    onSuccess: (updated) => {
      qc.setQueryData<ProjectResource[]>(
        projectResourceKeys.list(wsId, projectId),
        (old) => old?.map((resource) =>
          resource.id === updated.id ? updated : resource,
        ),
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
      const prev = qc.getQueryData<ProjectResource[]>(
        projectResourceKeys.list(wsId, projectId),
      );
      qc.setQueryData<ProjectResource[]>(
        projectResourceKeys.list(wsId, projectId),
        (old) => {
          if (!old || !old.some((resource) => resource.id === resourceId)) {
            return old;
          }
          return old.filter((resource) => resource.id !== resourceId);
        },
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
        queryKey: projectKeys.all(wsId),
      });
    },
  });
}
