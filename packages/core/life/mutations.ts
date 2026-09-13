// 本文件封装 life 模块的各类变更：设置/取消陪伴、记忆的确认/归档/删除/降级/
// 更新、提案确认、实验轮次的中止与复盘等。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import type { LifeMemoryKind, UpdateLifeMemoryRequest } from "../types";
import { lifeKeys } from "./queries";

/** 把某个 agent 设为（或取消）陪伴画像，成功后失效陪伴查询。 */
export function useSetCompanionProfile() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (agentId: string) => api.setCompanionProfile(agentId),
    onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.companion(wsId) }),
  });
}

/** 记忆类变更的公共骨架：settle 后统一失效记忆列表缓存。 */
function useMemoryMutation<T>(mutationFn: (input: T) => Promise<unknown>) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn,
    onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.memories(wsId) }),
  });
}

/** 确认一条记忆（让伴生 agent 把它沉淀进长期记忆）。 */
export const useConfirmLifeMemory = () => useMemoryMutation((id: string) => api.confirmLifeMemory(id));
/** 归档一条记忆。 */
export const useArchiveLifeMemory = () => useMemoryMutation((id: string) => api.archiveLifeMemory(id));
/** 删除一条记忆。 */
export const useDeleteLifeMemory = () => useMemoryMutation((id: string) => api.deleteLifeMemory(id));
/** 把一条记忆降级为指定的种类（例如从事件降为片段）。 */
export const useDowngradeLifeMemory = () => useMemoryMutation(({ id, kind }: { id: string; kind: LifeMemoryKind }) => api.downgradeLifeMemory(id, kind));
/** 更新一条记忆的字段。 */
export const useUpdateLifeMemory = () => useMemoryMutation(({ id, data }: { id: string; data: UpdateLifeMemoryRequest }) => api.updateLifeMemory(id, data));

/** 确认一条提案，成功后同时失效提案与实验缓存（确认提案通常会派生实验）。 */
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

/** 中止一轮实验并附上原因，成功后失效实验缓存。 */
export function useStopLifeExperimentRound() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) => api.stopLifeExperimentRound(id, reason),
    onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.experiments(wsId) }),
  });
}

/** 为一轮实验提交复盘（结果、感受、负担、对陪伴纠正），成功后失效实验缓存。 */
export function useReviewLifeExperimentRound() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string; outcome: string; feelings: string; burden: string; companion_correction: string }) => api.reviewLifeExperimentRound(id, data),
    onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.experiments(wsId) }),
  });
}
