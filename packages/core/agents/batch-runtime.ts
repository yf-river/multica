import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { cacheAgentResponse, workspaceKeys } from "../workspace/queries";

export type AgentRuntimeSettings = {
  runtime_id: string;
  model: string;
  thinking_level: string;
  service_tier: string;
};

export type AgentRuntimeResult = { id: string; error?: string };

/** 每个智能体一次提交完整配置，限制并发并保留逐项结果供失败重试。 */
export async function applyAgentRuntimeBatch(
  ids: readonly string[],
  settings: AgentRuntimeSettings,
  update: (id: string, settings: AgentRuntimeSettings) => Promise<unknown>,
  onProgress?: (completed: number) => void,
): Promise<AgentRuntimeResult[]> {
  const targets = [...new Set(ids)];
  const results: AgentRuntimeResult[] = new Array(targets.length);
  let next = 0;
  let completed = 0;
  await Promise.all(Array.from({ length: Math.min(4, targets.length) }, async () => {
    while (next < targets.length) {
      const index = next++;
      const id = targets[index]!;
      try {
        await update(id, settings);
        results[index] = { id };
      } catch (error) {
        results[index] = { id, error: error instanceof Error ? error.message : String(error) };
      }
      onProgress?.(++completed);
    }
  }));
  return results;
}

export function useBatchAgentRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      ids: string[];
      settings: AgentRuntimeSettings;
      onProgress?: (completed: number) => void;
    }) => applyAgentRuntimeBatch(input.ids, input.settings, async (id, settings) => {
      const agent = await api.updateAgent(id, settings);
      cacheAgentResponse(qc, wsId, agent, { insertIntoList: false });
    }, input.onProgress),
    onSettled: () => qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) }),
  });
}
