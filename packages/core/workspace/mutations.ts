import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { Workspace } from "../types";
import { api } from "../api";
import { workspaceKeys } from "./queries";
import { createWorkspaceWithRecovery } from "./create-operation";

export function useCreateWorkspace() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: { name: string; slug: string; description?: string }) =>
      createWorkspaceWithRecovery(data),
    // Seed the workspace list cache BEFORE callers navigate to /{newWs.slug}/issues.
    // The destination [workspaceSlug]/layout queries by slug from this cache;
    // without seeding, it would briefly show "loading" before the background
    // invalidation completes. TanStack Query guarantees this onSuccess runs
    // before mutateAsync's resolver / before any callback-style onSuccess
    // passed to mutate(), so any caller that navigates after the mutation
    // resolves will see the seeded data synchronously. Switching workspaces
    // is pure navigation now — no imperative store writes needed.
    onSuccess: (newWs) => {
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] = []) =>
        old.some((workspace) => workspace.id === newWs.id)
          ? old.map((workspace) => workspace.id === newWs.id ? newWs : workspace)
          : [...old, newWs],
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workspaceKeys.list() });
    },
  });
}

function useWorkspaceExitMutation(
  mutationFn: (workspaceId: string) => Promise<void>,
) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn,
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workspaceKeys.list() });
    },
  });
}

export function useLeaveWorkspace() {
  return useWorkspaceExitMutation((workspaceId) =>
    api.leaveWorkspace(workspaceId),
  );
}

export function useDeleteWorkspace() {
  return useWorkspaceExitMutation((workspaceId) =>
    api.deleteWorkspace(workspaceId),
  );
}
