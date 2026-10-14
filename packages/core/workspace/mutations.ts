// 本文件封装工作区相关的各类变更：创建工作区、离开工作区、删除工作区，以及
// 工作区/agent 的 MCP 服务器增删改。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { Workspace } from "../types";
import { api } from "../api";
import { defaultStorage } from "../platform/storage";
import { clearWorkspaceStorage } from "../platform/storage-cleanup";
import { workspaceKeys } from "./queries";
import {
  markWorkspaceDeletePending,
  unmarkWorkspaceDeletePending,
} from "./pending-delete";

/** 创建工作区，成功后把新工作区写进列表缓存（供随后的跳转立即按 slug 查到）。 */
export function useCreateWorkspace() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: {
      name: string;
      slug: string;
      description?: string;
      /** 省略时让服务端从 slug 推导。 */
      issue_prefix?: string;
    }) => api.createWorkspace(data),
    // 在调用方导航到 /{newWs.slug}/issues 之前就把新工作区种进列表缓存。
    // 目标 [workspaceSlug]/layout 从这份缓存按 slug 查询；不种的话，它会在
    // 后台失效完成前短暂显示「加载中」。TanStack Query 保证本 onSuccess 先于
    // mutateAsync 的 resolver / 任何传给 mutate() 的回调式 onSuccess 执行，
    // 所以任何在 mutation resolve 后导航的调用方都能同步看到种好的数据。
    // 切换工作区现在纯粹是导航——不需要任何命令式 store 写入。
    onSuccess: (newWs) => {
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] = []) => [...old, newWs]);
    },
    // 创建失败不应改变服务端状态，但还是要失效一次列表，去对账「服务端可能
    // 已提交、客户端却丢了响应」这类模糊的传输失败。别放在成功路径上，免得
    // 上面种下的规范响应又被重新拉一次。
    onError: () => {
      qc.invalidateQueries({ queryKey: workspaceKeys.list() });
    },
  });
}

/** 离开某个工作区，成功后失效工作区列表缓存。 */
export function useLeaveWorkspace() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (workspaceId: string) => api.leaveWorkspace(workspaceId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workspaceKeys.list() });
    },
  });
}

/** 删除工作区：删除成功后清理该工作区的本地存储命名空间。 */
export function useDeleteWorkspace() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (workspaceId: string) => api.deleteWorkspace(workspaceId),
    // 不做乐观移除：删除流程会带着确认对话框的加载态 await 本次 mutation，
    // 只在成功时导航，所以缓存保持如实（提交前服务端仍有这行）是对的，
    // DELETE 失败也不需要回滚。
    onMutate: (workspaceId) => {
      // 把删除标记为「本客户端发起」，让实时 `workspace:deleted` 处理器空跑，
      // 别用它的整页跳转和本流程的导航赛跑。生命周期规则见 pending-delete.ts。
      markWorkspaceDeletePending(workspaceId);
      // 顺带在 onMutate 里捕获 slug 供 onSuccess 做存储清理——此刻零成本，
      // 且 mutation 前该行保证仍在列表里。
      const slug = qc
        .getQueryData<Workspace[]>(workspaceKeys.list())
        ?.find((w) => w.id === workspaceId)?.slug;
      return { slug };
    },
    // 只有成功路径会清掉被删工作区持久化的 `${key}:${slug}` 命名空间——失败的
    // DELETE 意味着工作区还在，它的草稿/视图状态必须存活。实时处理器会跳过
    // 本客户端发起的删除，所以清理只能在这里做。
    onSuccess: (_data, _workspaceId, ctx) => {
      if (ctx?.slug) clearWorkspaceStorage(defaultStorage, ctx.slug);
    },
    // DELETE 失败后工作区仍存在，所以之后同 id 被外部删除必须重新交给实时
    // 处理器正常处理。
    onError: (_err, workspaceId) => {
      unmarkWorkspaceDeletePending(workspaceId);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workspaceKeys.list() });
    },
  });
}

/**
 * 往工作区库加一台 MCP 服务器。它不指派给任何 agent——agent 的所有者会单独
 * 把它配给自己的 agent。
 */
export function useCreateWorkspaceMcpServer(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ name, config, description }: { name: string; config: Record<string, unknown>; description?:string }) =>
      api.createWorkspaceMcpServer(wsId, name, config, description),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: workspaceKeys.mcpServers(wsId) }),
  });
}

/** 更新工作区库里的一台 MCP 服务器。 */
export function useUpdateWorkspaceMcpServer(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ serverId, ...update }: {
      serverId: string;
      name?: string;
      config?: Record<string, unknown>;
    }) => api.updateWorkspaceMcpServer(wsId, serverId, update),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: workspaceKeys.mcpServers(wsId) }),
  });
}

/** 删除工作区库里的一台 MCP 服务器。 */
export function useDeleteWorkspaceMcpServer(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (serverId: string) => api.deleteWorkspaceMcpServer(wsId, serverId),
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: workspaceKeys.mcpServers(wsId) });
      // 删除库条目会把它从每个配过它的 agent 上摘掉，之后任何 agent 的指派
      // 列表都不再可信。
      queryClient.invalidateQueries({ queryKey: ["agents"] });
    },
  });
}

/**
 * agent MCP 指派变更的公共骨架。指派类写入都会返回该 agent 的最终列表，
 * 所以这里总是让缓存用服务端的答案更新，而不是用猜测。
 */
function useAgentMcpMutation<TVariables>(
  agentId: string,
  mutationFn: (variables: TVariables) => Promise<unknown>,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn,
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: ["agents", agentId, "mcp-servers"] }),
  });
}

/** 给 agent 加一台 MCP 服务器。 */
export function useAddAgentMcpServer(agentId: string) {
  return useAgentMcpMutation(agentId, (serverId: string) =>
    api.addAgentMcpServer(agentId, serverId));
}

/** 启停 agent 上的一台 MCP 服务器。 */
export function useSetAgentMcpServerEnabled(agentId: string) {
  return useAgentMcpMutation(agentId, ({ serverId, enabled }: { serverId: string; enabled: boolean }) =>
    api.setAgentMcpServerEnabled(agentId, serverId, enabled));
}

/** 从 agent 上移除一台 MCP 服务器。 */
export function useRemoveAgentMcpServer(agentId: string) {
  return useAgentMcpMutation(agentId, (serverId: string) =>
    api.removeAgentMcpServer(agentId, serverId));
}
