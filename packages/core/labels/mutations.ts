import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { labelKeys } from "./queries";
import { useWorkspaceId } from "../paths";
import { issueKeys } from "../issues/queries";
import { onIssueLabelsChanged } from "../issues/ws-updaters";
import type {
  Label,
  CreateLabelRequest,
  UpdateLabelRequest,
} from "../types";

export function useCreateLabel() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreateLabelRequest) => api.createLabel(data),
    onSuccess: (label) => {
      qc.setQueryData<Label[]>(labelKeys.list(wsId), (old) =>
        old && !old.some((item) => item.id === label.id)
          ? [...old, label]
          : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: labelKeys.list(wsId) });
    },
  });
}

/**
 * Optimistic rename/recolor. Matches the useUpdateProject pattern: apply the
 * change locally, snapshot for rollback, invalidate on settle. Without this
 * the UI freezes for the round-trip on every edit.
 */
export function useUpdateLabel() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string } & UpdateLabelRequest) =>
      api.updateLabel(id, data),
    onMutate: async ({ id, ...data }) => {
      await qc.cancelQueries({ queryKey: labelKeys.list(wsId) });
      const prevList = qc.getQueryData<Label[]>(labelKeys.list(wsId));
      qc.setQueryData<Label[]>(labelKeys.list(wsId), (old) =>
        old?.map((label) => label.id === id ? { ...label, ...data } : label),
      );
      return { prevList, id };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prevList) qc.setQueryData(labelKeys.list(wsId), ctx.prevList);
    },
    onSettled: () => {
      // Invalidate the entire labels scope so any byIssue cache holding a
      // stale copy of this label is refetched. The list cache is the source
      // of truth; byIssue views will re-render with the fresh data.
      qc.invalidateQueries({ queryKey: labelKeys.all(wsId) });
      // Issues now embed labels (denormalized snapshot), so a rename/recolor
      // also has to refresh the issues caches that hold those snapshots.
      qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
    },
  });
}

export function useDeleteLabel() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.deleteLabel(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: labelKeys.list(wsId) });
      const prev = qc.getQueryData<Label[]>(labelKeys.list(wsId));
      qc.setQueryData<Label[]>(labelKeys.list(wsId), (old) =>
        old?.filter((label) => label.id !== id),
      );
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) qc.setQueryData(labelKeys.list(wsId), ctx.prev);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: labelKeys.all(wsId) });
      // A deleted label still lives in cached issue.labels arrays until we
      // refetch — invalidate so list/board chips drop the orphan.
      qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
    },
  });
}

export function useAttachLabel(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (labelId: string) => api.attachLabel(issueId, labelId),
    onMutate: async (labelId) => {
      await qc.cancelQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });
      const prev = qc.getQueryData<Label[]>(labelKeys.byIssue(wsId, issueId));
      // Only patch when we already know the current label set — otherwise
      // appending `[label]` to an empty array would wipe denormalized
      // labels in issue list/detail caches and rollback couldn't restore
      // them. If byIssue isn't cached yet (user clicked before the picker
      // fetched), skip the optimistic patch and rely on onSettled refetch.
      if (!prev) return { prev };
      if (prev.some((label) => label.id === labelId)) return { prev };
      const list = qc.getQueryData<Label[]>(labelKeys.list(wsId));
      const label = list?.find((item) => item.id === labelId);
      if (!label) return { prev };
      const next = [...prev, label];
      qc.setQueryData<Label[]>(labelKeys.byIssue(wsId, issueId), next);
      onIssueLabelsChanged(qc, wsId, issueId, next);
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) {
        qc.setQueryData(labelKeys.byIssue(wsId, issueId), ctx.prev);
        onIssueLabelsChanged(qc, wsId, issueId, ctx.prev);
      }
    },
    onSuccess: (labels) => {
      qc.setQueryData<Label[]>(labelKeys.byIssue(wsId, issueId), labels);
      onIssueLabelsChanged(qc, wsId, issueId, labels);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });
    },
  });
}

export function useDetachLabel(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (labelId: string) => api.detachLabel(issueId, labelId),
    onMutate: async (labelId) => {
      await qc.cancelQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });
      const prev = qc.getQueryData<Label[]>(labelKeys.byIssue(wsId, issueId));
      const next = prev?.filter((label) => label.id !== labelId);
      if (next) {
        qc.setQueryData<Label[]>(labelKeys.byIssue(wsId, issueId), next);
        onIssueLabelsChanged(qc, wsId, issueId, next);
      }
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) {
        qc.setQueryData(labelKeys.byIssue(wsId, issueId), ctx.prev);
        onIssueLabelsChanged(qc, wsId, issueId, ctx.prev);
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });
    },
  });
}
