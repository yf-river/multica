// 本文件封装「项目资源」（项目详情里的关联资源）的 React Query 查询与变更：
// 列表、创建、更新、删除。

import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { projectKeys } from "./queries";
import type {
  CreateProjectResourceRequest,
  ListProjectResourcesResponse,
  ProjectResource,
  UpdateProjectResourceRequest,
} from "../types";

/** 项目资源查询的 query key 命名空间（挂在项目详情 key 之下）。 */
export const projectResourceKeys = {
  list: (wsId: string, projectId: string) =>
    [...projectKeys.detail(wsId, projectId), "resources"] as const,
};

/** 查询某个项目下的资源列表。 */
export function projectResourcesOptions(wsId: string, projectId: string) {
  return queryOptions({
    queryKey: projectResourceKeys.list(wsId, projectId),
    queryFn: () => api.listProjectResources(projectId),
    select: (data) => data.resources,
  });
}

/** 创建一条项目资源，成功后写进本地缓存列表（按 id 去重）。 */
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
      void qc.invalidateQueries({queryKey:["workspace-repositories",wsId]});
      void qc.invalidateQueries({queryKey:["workspaces"]});
      qc.invalidateQueries({
        queryKey: projectResourceKeys.list(wsId, projectId),
      });
    },
  });
}

/** 更新一条项目资源，成功后用返回值替换本地缓存里的对应项。 */
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
      void qc.invalidateQueries({queryKey:["workspace-repositories",wsId]});
      void qc.invalidateQueries({queryKey:["workspaces"]});
      qc.invalidateQueries({
        queryKey: projectResourceKeys.list(wsId, projectId),
      });
    },
  });
}

/** 删除一条项目资源，乐观从本地缓存移除并支持回滚。 */
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
      void qc.invalidateQueries({queryKey:["workspace-repositories",wsId]});
      void qc.invalidateQueries({queryKey:["workspaces"]});
      qc.invalidateQueries({
        queryKey: projectResourceKeys.list(wsId, projectId),
      });
    },
  });
}
