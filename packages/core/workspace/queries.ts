// 本文件封装工作区相关的 React Query 查询：工作区列表、成员/agent/squad 目录、
// 邀请、分享链接、技能、MCP 服务器等。

import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { Agent, Squad, Workspace } from "../types";

/** 工作区相关查询的 query key 命名空间。 */
export const workspaceKeys = {
  all: (wsId: string) => ["workspaces", wsId] as const,
  list: () => ["workspaces", "list"] as const,
  members: (wsId: string) => ["workspaces", wsId, "members"] as const,
  invitations: (wsId: string) => ["workspaces", wsId, "invitations"] as const,
  shareLinks: (wsId: string) => ["workspaces", wsId, "share-links"] as const,
  myInvitations: () => ["invitations", "mine"] as const,
  agents: (wsId: string) => ["workspaces", wsId, "agents"] as const,
  agent: (wsId: string, agentId: string) =>
    ["workspaces", wsId, "agents", "detail", agentId] as const,
  squads: (wsId: string) => ["workspaces", wsId, "squads"] as const,
  // 每个 squad 的成员状态。放在工作区 key 树下，这样切换工作区天然会丢缓存，
  // 一次宽泛的 `["workspaces", wsId, "squads"]` 失效也能覆盖它。
  squadMemberStatus: (wsId: string, squadId: string) =>
    ["workspaces", wsId, "squads", squadId, "members-status"] as const,
  skills: (wsId: string) => ["workspaces", wsId, "skills"] as const,
  assigneeFrequency: (wsId: string) => ["workspaces", wsId, "assignee-frequency"] as const,
  mcpServers: (wsId: string) => ["workspaces", wsId, "mcp-servers"] as const,
};

/** 查询当前用户所属的全部工作区。 */
export function workspaceListOptions() {
  return queryOptions({
    queryKey: workspaceKeys.list(),
    queryFn: () => api.listWorkspaces(),
  });
}

/** 从缓存的工作区列表里解析 slug 匹配的工作区。 */
export function workspaceBySlugOptions(slug: string) {
  return queryOptions({
    ...workspaceListOptions(),
    select: (list: Workspace[]) => list.find((w) => w.slug === slug) ?? null,
  });
}

/** 查询工作区成员列表。 */
export function memberListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.members(wsId),
    queryFn: () => api.listMembers(wsId),
  });
}

/** 查询工作区的 agent 列表（含已归档，供选择器用）。 */
export function agentListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.agents(wsId),
    queryFn: () =>
      api.listAgents({ workspace_id: wsId, include_archived: true }),
  });
}

/**
 * 查询工作区的托管 Agent（如审查员）。它们不出现在选择器里，只用于把评论/活动
 * 的作者 id 解析成可读名字；与 agentListOptions 分开缓存，避免混进可选目录。
 */
export function managedAgentListOptions(wsId: string) {
  return queryOptions({
    queryKey: [...workspaceKeys.agents(wsId), "managed"] as const,
    queryFn: () => api.listAgents({ workspace_id: wsId, include_managed: true }),
  });
}

/** 查询单个 agent 详情。 */
export function agentDetailOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: workspaceKeys.agent(wsId, agentId),
    queryFn: () => api.getAgent(agentId),
    enabled: !!wsId && !!agentId,
    retry: false,
  });
}

/**
 * 把一份权威的 agent 响应立刻同步给 agent 详情与列表两类消费方。
 * 冷着的列表保持冷着——单个 agent 绝不能被误当成完整的工作区列表。
 */
export function cacheAgentResponse(
  queryClient: QueryClient,
  wsId: string,
  agent: Agent,
  options: { insertIntoList?: boolean } = {},
) {
  queryClient.setQueryData(workspaceKeys.agent(wsId, agent.id), agent);
  queryClient.setQueryData<Agent[]>(workspaceKeys.agents(wsId), (current) => {
    if (!current) return current;
    const existingIndex = current.findIndex((item) => item.id === agent.id);
    if (existingIndex === -1) {
      return options.insertIntoList === false ? current : [...current, agent];
    }
    return current.map((item, index) =>
      index === existingIndex ? agent : item,
    );
  });
}

/** 查询工作区 squad 列表。 */
export function squadListOptions(wsId: string) {
  return queryOptions<Squad[]>({
    queryKey: workspaceKeys.squads(wsId),
    queryFn: () => api.listSquads(),
    enabled: !!wsId,
  });
}

// 每个 squad 的成员状态快照。新鲜度信号来自 use-realtime-sync 里接的 WS
// task/agent/runtime 失效（会宽泛地失效 `["workspaces", wsId, "squads"]`）；
// staleTime 是「标签页重新聚焦」的安全网。
export function squadMemberStatusOptions(wsId: string, squadId: string) {
  return queryOptions({
    queryKey: workspaceKeys.squadMemberStatus(wsId, squadId),
    queryFn: () => api.getSquadMemberStatus(squadId),
    enabled: !!wsId && !!squadId,
    staleTime: 30 * 1000,
    refetchOnWindowFocus: true,
  });
}

/** 查询工作区的技能列表。 */
export function skillListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.skills(wsId),
    queryFn: () => api.listSkills(),
  });
}

/** 查询单个技能详情。 */
export function skillDetailOptions(wsId: string, skillId: string) {
  return queryOptions({
    queryKey: [...workspaceKeys.skills(wsId), skillId] as const,
    queryFn: () => api.getSkill(skillId),
    enabled: !!skillId,
  });
}

/**
 * 从缓存里的 agent 列表构建 `Map<skillId, Agent[]>`。
 *
 * 服务端已把每个 agent 的完整技能列表内联返回，无需额外请求——「哪些 agent
 * 在用技能 X」是纯客户端归并。
 *
 * 暴露成普通辅助函数而不是带 `select` 的 `queryOptions`，是为了让 Map 的引用
 * 在不相关的 agent 缓存重渲染间保持稳定——调用方用 `useMemo(..., [agents])`
 * 包一层，只在 agent 数组引用真变时才重新归并。旧实现是 `{ select }`，每次订阅
 * 心跳都返回新 Map，导致每一条 `agent:updated` WS 事件都引发连锁重渲染。
 */
export function selectSkillAssignments(
  agents: Agent[] | undefined,
): Map<string, Agent[]> {
  const map = new Map<string, Agent[]>();
  if (!agents) return map;
  for (const a of agents) {
    if (a.archived_at) continue;
    for (const s of a.skills ?? []) {
      const existing = map.get(s.id);
      if (existing) existing.push(a);
      else map.set(s.id, [a]);
    }
  }
  return map;
}

/** 查询工作区邀请列表。 */
export function invitationListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.invitations(wsId),
    queryFn: () => api.listWorkspaceInvitations(wsId),
  });
}

/** 查询工作区的分享链接列表。 */
export function shareLinkListOptions(wsId: string, enabled = true) {
  return queryOptions({
    queryKey: workspaceKeys.shareLinks(wsId),
    queryFn: () => api.listShareLinks(wsId),
    enabled: enabled && !!wsId,
  });
}

/** 查询当前用户收到的邀请列表。 */
export function myInvitationListOptions() {
  return queryOptions({
    queryKey: workspaceKeys.myInvitations(),
    queryFn: () => api.listMyInvitations(),
  });
}

/** 查询指派频率统计（谁是工作区里被派活最多的人等）。 */
export function assigneeFrequencyOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.assigneeFrequency(wsId),
    queryFn: () => api.getAssigneeFrequency(),
  });
}

/**
 * 工作区的 MCP 服务器库。普通成员也会拉取——负载只有名字与传输方式，
 * 而 agent 的所有者需要看到可指派的东西。
 */
export function workspaceMcpServersOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.mcpServers(wsId),
    queryFn: () => api.listWorkspaceMcpServers(wsId),
    enabled: wsId !== "",
  });
}

/** 分配给某个 agent 的工作区 MCP 服务器（带各自的启停开关）。 */
export function agentMcpServersOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agents", agentId, "mcp-servers"] as const,
    queryFn: () => api.listAgentMcpServers(agentId),
    enabled: agentId !== "",
  });
}
