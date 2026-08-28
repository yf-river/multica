import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../paths";
import type { LifeMemoryKind, UpdateLifeMemoryRequest } from "../types";
import { lifeKeys } from "./queries";

export function useSetCompanionProfile() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (agentId: string) => api.setCompanionProfile(agentId),
    onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.companion(wsId) }),
  });
}

function useMemoryMutation<T>(mutationFn: (input: T) => Promise<unknown>) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn,
    onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.memories(wsId) }),
  });
}

export const useConfirmLifeMemory = () => useMemoryMutation((id: string) => api.confirmLifeMemory(id));
export const useArchiveLifeMemory = () => useMemoryMutation((id: string) => api.archiveLifeMemory(id));
export const useDeleteLifeMemory = () => useMemoryMutation((id: string) => api.deleteLifeMemory(id));
export const useDowngradeLifeMemory = () => useMemoryMutation(({ id, kind }: { id: string; kind: LifeMemoryKind }) => api.downgradeLifeMemory(id, kind));
export const useUpdateLifeMemory = () => useMemoryMutation(({ id, data }: { id: string; data: UpdateLifeMemoryRequest }) => api.updateLifeMemory(id, data));

export function useConfirmLifeProposal() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.confirmLifeProposal(id),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: lifeKeys.proposals(wsId) });
      qc.invalidateQueries({ queryKey: lifeKeys.experiments(wsId) });
    },
  });
}

export function useStopLifeExperimentRound() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) => api.stopLifeExperimentRound(id, reason),
    onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.experiments(wsId) }),
  });
}

export function useReviewLifeExperimentRound() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string; outcome: string; feelings: string; burden: string; companion_correction: string }) => api.reviewLifeExperimentRound(id, data),
    onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.experiments(wsId) }),
  });
}
