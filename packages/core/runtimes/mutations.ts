// Runtime 实例的变更 mutation：删除、强制解绑删除、补丁更新。
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { runtimeKeys } from "./queries";
import { workspaceKeys } from "../workspace/queries";
import { agentTaskSnapshotKeys } from "../agents/queries";

/** 删除一台 runtime 的 mutation hook（成功后失效 runtime 缓存）。 */
export function useDeleteRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (runtimeId: string) => api.deleteRuntime(runtimeId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}

// useDeleteRuntime 的"确认删除"版本。弹窗在严格 DELETE 以 `runtime_has_active_agents`
// 拒绝时（或调用方已知该 runtime 有活跃 Agent、想跳过预检拒绝时）走这里。
// mutation 函数返回服务器报告的计数，让调用方能渲染更丰富的成功 toast。
//
// 失效 runtimes（列表/详情）、工作区 agents（它们被解绑，runtime 列与就绪状态会变）
// 与 agent 呈现快照（删除也会取消排队/运行中的任务）。没有 Agent 侧失效，Agents 页
// 会继续把它们显示成可运行。
/** 强制解绑全部 Agent 后删除 runtime 的 mutation hook。 */
export function useUnbindAgentsAndDeleteRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      runtimeId,
      expectedActiveAgentIds,
    }: {
      runtimeId: string;
      expectedActiveAgentIds: string[];
    }) => api.unbindAgentsAndDeleteRuntime(runtimeId, expectedActiveAgentIds),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      qc.invalidateQueries({ queryKey: agentTaskSnapshotKeys.all(wsId) });
    },
  });
}

// Confirmed public→private transition. Like runtime deletion, this changes
// runtime rows, agent bindings, and task presence, so every affected server
// projection is invalidated after the transaction settles.
export function useRevokeRuntimeWorkspaceAccess(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      runtimeId,
      expectedNonownerAgentIds,
      expectedTaskIds,
    }: {
      runtimeId: string;
      expectedNonownerAgentIds: string[];
      expectedTaskIds: string[];
    }) =>
      api.revokeRuntimeWorkspaceAccess(
        runtimeId,
        expectedNonownerAgentIds,
        expectedTaskIds,
      ),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      qc.invalidateQueries({ queryKey: agentTaskSnapshotKeys.all(wsId) });
    },
  });
}

// useUpdateRuntime patches editable fields on a runtime (visibility, custom
// name). Invalidates the runtime list so the picker disabled-state and
// display names recompute.
export function useUpdateRuntime(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      runtimeId,
      patch,
    }: {
      runtimeId: string;
      patch: {
        visibility?: "private" | "public";
        // 空字符串清除自定义名；省略则保持不变。
        custom_name?: string;
        apply_to_machine?: boolean;
      };
    }) => api.updateRuntime(runtimeId, patch),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}
