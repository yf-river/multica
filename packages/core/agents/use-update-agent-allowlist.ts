import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import type { Agent } from "../types";
import { workspaceKeys } from "../workspace/queries";

/**
 * 仅创建者可用的 MCP 标签页用的 mutation hook：经 `PUT /api/agents/:id`
 * （{ composio_toolkit_allowlist }）写 Agent 的 Composio 工具包白名单——
 * 没有专用接口，走既有 Agent PATCH 路径（MUL-3870）。
 *
 * hook 是乐观的：往返完成前先修补缓存里工作区列表中匹配的 Agent，让复选框瞬间
 * 翻转；出错回滚到抓取的快照，settle 时总是失效让缓存与服务器的规范化 slug
 * （小写/去空格/去重）重新对齐。服务器会静默丢弃非所有者的写入——所以它只接进
 * 了"仅所有者"的标签页。
 *
 * 参数是完整的期望白名单（`string[]`）——调用方自己算下一个数组（加/删一个 slug）
 * 再整包传入，与后端"整体替换"的语义一致。传 `[]` 清空全部工具包。
 * @param agentId 目标 Agent id。
 * @returns React Query mutation 对象。
 */
export function useUpdateAgentAllowlist(agentId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation<Agent, Error, string[], { previous?: Agent[] }>({
    mutationFn: (allowlist) =>
      api.updateAgent(agentId, { composio_toolkit_allowlist: allowlist }),
    onMutate: async (allowlist) => {
      const queryKey = workspaceKeys.agents(wsId);
      // 取消飞行中的重取，防止它们踩掉乐观写入。
      await qc.cancelQueries({ queryKey });
      const previous = qc.getQueryData<Agent[]>(queryKey);
      qc.setQueryData<Agent[]>(queryKey, (old) =>
        old?.map((a) =>
          a.id === agentId
            ? ({ ...a, composio_toolkit_allowlist: allowlist } as Agent)
            : a,
        ),
      );
      return { previous };
    },
    onError: (_error, _allowlist, context) => {
      // 失败：把抓取的旧列表放回去。
      if (context?.previous) {
        qc.setQueryData(workspaceKeys.agents(wsId), context.previous);
      }
    },
    onSettled: () => {
      // 无论成败都失效重取，与服务器规范化的结果对齐。
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    },
  });
}
