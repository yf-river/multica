import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { executeRecoverableMutation } from "../api/transport";
import { projectKeys } from "./queries";
import { useWorkspaceId } from "../paths";
import { useRecentContextStore } from "../chat/recent-context-store";
import type { Project, CreateProjectRequest, UpdateProjectRequest } from "../types";
import { generateUUID } from "../utils";
import { useProjectDraftStore } from "./draft-store";

export function useCreateProject() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: async (data: CreateProjectRequest) => {
      const draftStore = useProjectDraftStore.getState();
      const pendingCreate = draftStore.draft.pendingCreate ?? {
        requestKey: generateUUID(),
        request: data,
      };
      draftStore.setDraft({ pendingCreate });
      return executeRecoverableMutation(
        () => api.createProject(
          pendingCreate.request,
          pendingCreate.requestKey,
        ),
        () => useProjectDraftStore.getState().setDraft({ pendingCreate: undefined }),
      );
    },
    onSuccess: (newProject) => {
      qc.setQueryData<Project[]>(projectKeys.list(wsId), (old) =>
        old && !old.some((p) => p.id === newProject.id)
          ? [...old, newProject]
          : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: projectKeys.list(wsId) });
    },
  });
}

export function useUpdateProject() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string } & UpdateProjectRequest) =>
      api.updateProject(id, data),
    onMutate: ({ id, ...data }) => {
      qc.cancelQueries({ queryKey: projectKeys.list(wsId) });
      const prevList = qc.getQueryData<Project[]>(projectKeys.list(wsId));
      const prevDetail = qc.getQueryData<Project>(projectKeys.detail(wsId, id));
      qc.setQueryData<Project[]>(projectKeys.list(wsId), (old) =>
        old?.map((p) => (p.id === id ? { ...p, ...data } : p)),
      );
      qc.setQueryData<Project>(projectKeys.detail(wsId, id), (old) =>
        old ? { ...old, ...data } : old,
      );
      return { prevList, prevDetail, id };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prevList) qc.setQueryData(projectKeys.list(wsId), ctx.prevList);
      if (ctx?.prevDetail) qc.setQueryData(projectKeys.detail(wsId, ctx.id), ctx.prevDetail);
    },
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: projectKeys.detail(wsId, vars.id) });
      qc.invalidateQueries({ queryKey: projectKeys.list(wsId) });
    },
  });
}

export function useDeleteProject() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.deleteProject(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: projectKeys.list(wsId) });
      const prevList = qc.getQueryData<Project[]>(projectKeys.list(wsId));
      qc.setQueryData<Project[]>(projectKeys.list(wsId), (old) =>
        old?.filter((p) => p.id !== id),
      );
      qc.removeQueries({ queryKey: projectKeys.detail(wsId, id) });
      return { prevList };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prevList) qc.setQueryData(projectKeys.list(wsId), ctx.prevList);
    },
    onSuccess: (_data, id) => {
      useRecentContextStore.getState().forgetContext(wsId, { type: "project", id });
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: projectKeys.list(wsId) });
    },
  });
}
