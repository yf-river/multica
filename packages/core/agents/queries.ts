// Agent 相关查询：query key 工厂与 queryOptions。覆盖任务快照、工作中 Agent、
// 近 30 天活动/运行计数、单 Agent 全部任务、以及未完成的 Agent 构建会话。
import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type {
  WorkspaceWorkingAgentMineRelation,
  WorkspaceWorkingAgentType,
} from "../types";

/** Agent 任务快照的缓存键工厂。 */
export const agentTaskSnapshotKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-task-snapshot"] as const,
  list: (wsId: string) => [...agentTaskSnapshotKeys.all(wsId), "list"] as const,
};

/** "工作中 Agent"摘要的缓存键工厂。 */
export const workspaceWorkingAgentsKeys = {
  all: (wsId: string) => ["workspaces", wsId, "working-agents"] as const,
  list: (
    wsId: string,
    type?: WorkspaceWorkingAgentType,
    mineRelation?: WorkspaceWorkingAgentMineRelation,
    parentIssueId?: string,
  ) =>
    [
      ...workspaceWorkingAgentsKeys.all(wsId),
      "list",
      type ?? "all",
      // 收窄维度编码进 key：my 关系、特定父议题的子议题、或纯工作区级。
      mineRelation
        ? `mine:${mineRelation}`
        : parentIssueId
          ? `parent:${parentIssueId}`
          : "workspace",
    ] as const,
};

/** Agent 日活数据的缓存键工厂。 */
export const agentActivityKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-activity"] as const,
  last30d: (wsId: string) => [...agentActivityKeys.all(wsId), "30d"] as const,
};

/** Agent 运行计数数据的缓存键工厂。 */
export const agentRunCountsKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-run-counts"] as const,
  last30d: (wsId: string) => [...agentRunCountsKeys.all(wsId), "30d"] as const,
};

// 工作区级 Agent 任务快照——每个活跃任务 + 每个 Agent 最近一条终态任务。
// 这是驱动全应用逐 Agent"呈现状态"推导的单一共享真相来源：每个工作区只拉一次，
// 所有 Agent 圆点/悬停卡/列表行都从这份缓存推导呈现状态，零额外网络请求。
//
// 呈现状态本身只从活跃任务推导（见 derive-presence.ts 和 #1823）。每个 Agent 的
// 那一条终态行仅用于 Squad 悬停卡上的"最近活动"行；MUL-5436 打算把它挪到专门的
// 懒加载接口，让这条热查询彻底不再携带历史。
//
// 30s 的 staleTime 只是安全网；主要的新鲜度信号是 WS 任务事件，它会立刻失效此查询。
// 没有 WS 时，呈现状态在聚焦/挂载时仍会 30s 内更新。
/** Agent 任务快照查询配置（工作区级一次拉取，全应用共享）。 */
export function agentTaskSnapshotOptions(wsId: string) {
  return queryOptions({
    queryKey: agentTaskSnapshotKeys.list(wsId),
    queryFn: () => api.getAgentTaskSnapshot(),
    staleTime: 30 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

// "工作中 Agent"摘要，可选收窄到某个 My Issues 关系、或某议题的直接子议题。
// 任务生命周期 WS 事件会立即失效所有收窄；短 staleTime 是重连/漏事件的兜底。
/** "工作中 Agent"摘要查询配置（可按类型/我的关系/父议题收窄）。 */
export function workspaceWorkingAgentsOptions(
  wsId: string,
  type?: WorkspaceWorkingAgentType,
  mineRelation?: WorkspaceWorkingAgentMineRelation,
  parentIssueId?: string,
) {
  return queryOptions({
    queryKey: workspaceWorkingAgentsKeys.list(
      wsId,
      type,
      mineRelation,
      parentIssueId,
    ),
    queryFn: () =>
      api.getWorkspaceWorkingAgents(type, mineRelation, parentIssueId),
    staleTime: 30 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

// 工作区近 30 天、锚定在 completed_at 上的逐日任务活动。一次拉取同时支撑
// Agents 列表的小火花图（经 `summarizeActivityWindow` 只用尾部 7 个桶）和
// Agent 详情页的"近 30 天"面板。WS 任务生命周期事件在 useRealtimeSync 里失效
// 该查询；staleTime 是切回标签页时的兜底。
/** 近 30 天任务活动查询配置。 */
export function agentActivity30dOptions(wsId: string) {
  return queryOptions({
    queryKey: agentActivityKeys.last30d(wsId),
    queryFn: () => api.getWorkspaceAgentActivity30d(),
    staleTime: 60 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

// 工作区近 30 天运行计数，供 Agents 列表的 RUNS 列使用。与 activity24hOptions
// 相同的"单次拉取 / WS 失效"模式。
/** 近 30 天运行计数查询配置。 */
export function agentRunCounts30dOptions(wsId: string) {
  return queryOptions({
    queryKey: agentRunCountsKeys.last30d(wsId),
    queryFn: () => api.getWorkspaceAgentRunCounts(),
    staleTime: 60 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

/** 单 Agent 任务列表的缓存键工厂。 */
export const agentTasksKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-tasks"] as const,
  detail: (wsId: string, agentId: string) =>
    [...agentTasksKeys.all(wsId), agentId] as const,
};

// 单个 Agent 的全部任务（Agent 详情页消费方）。同时支撑检查器的 7 天吞吐统计和
// Tasks 标签页列表——共享同一查询，避免拉两遍。WS 任务事件通过 useRealtimeSync
// 里已有的 task 前缀失效逻辑来失效本查询。
/** 单个 Agent 的全部任务查询配置。 */
export function agentTasksOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: agentTasksKeys.detail(wsId, agentId),
    queryFn: () => api.listAgentTasks(agentId),
    staleTime: 30 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  });
}

/** 未完成的 Agent 创建会话（按当前调用者作用域）的缓存键工厂。 */
export const agentBuilderSessionKeys = {
  all: (wsId: string) => ["workspace", wsId, "agent-builder-sessions"] as const,
  list: (wsId: string) => [...agentBuilderSessionKeys.all(wsId), "list"] as const,
};

/** 未完成的 Agent 创建会话列表查询配置。 */
export function agentBuilderSessionListOptions(wsId: string) {
  return queryOptions({
    queryKey: agentBuilderSessionKeys.list(wsId),
    queryFn: () => api.listAgentBuilderSessions(),
    enabled: wsId.length > 0,
    // 覆盖客户端全局的 `staleTime: Infinity`。这份列表会随着另一个屏幕上的工作而
    // 变化——发起对话、发一轮消息、Agent 终于被创建——而渲染它的界面是按需挂载的。
    // 永远缓存会展示第一次访问的状态：刚聊过一场的用户回来只看到"没有草稿"。
    staleTime: 0,
  });
}

export function taskOutputOptions(wsId: string, taskId: string, messageId: string, offset: number, query: string) {
  return queryOptions({
    queryKey: ["workspaces", wsId, "task-output", taskId, messageId, offset, query],
    queryFn: async () => {
      const page = await api.readTaskOutput(taskId, messageId, offset, query);
      if (!page) throw new Error("Invalid task output response");
      return page;
    },
    enabled: !!wsId && !!taskId && !!messageId,
    staleTime: Infinity,
  });
}
