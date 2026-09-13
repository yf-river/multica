// 本文件封装钉钉集成相关的 React Query 查询：安装列表、群列表、agent 群绑定、
// 无活跃的群（分页）等，供「设置 → 集成」面板与 agent 详情页使用。

import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/**
 * 所有钉钉安装相关查询的 query key 命名空间。实时同步会在收到
 * `dingtalk_installation:*` 事件时失效 `installations(wsId)`，这样设置面板
 * 无需手动刷新就能更新（例如在另一个标签页完成绑定时）。
 */
export const dingtalkKeys = {
  all: (wsId: string) => ["dingtalk", wsId] as const,
  installations: (wsId: string) => [...dingtalkKeys.all(wsId), "installations"] as const,
  groups: (wsId: string) => [...dingtalkKeys.all(wsId), "groups"] as const,
  agentGroups: (wsId: string, agentId: string) =>
    [...dingtalkKeys.groups(wsId), "agent", agentId] as const,
  inactiveGroups: (wsId: string, installationId: string) =>
    [...dingtalkKeys.groups(wsId), "inactive", installationId] as const,
  agentInactiveGroups: (wsId: string, agentId: string, installationId: string) =>
    [...dingtalkKeys.agentGroups(wsId, agentId), "inactive", installationId] as const,
};

/** 查询工作区下的全部钉钉安装记录。 */
export const dingtalkInstallationsOptions = (wsId: string) =>
  queryOptions({
    queryKey: dingtalkKeys.installations(wsId),
    queryFn: () => api.listDingTalkInstallations(wsId),
    enabled: !!wsId,
  });

/** 查询工作区下的钉钉群（仅权限允许的「设置」清单使用）。 */
export const dingtalkGroupsOptions = (wsId: string) =>
  queryOptions({
    queryKey: dingtalkKeys.groups(wsId),
    queryFn: () => api.listDingTalkGroups(wsId),
    enabled: !!wsId,
    // 群发现是通过钉钉 Stream 回调推过来的，不是 HTTP 变更，所以在权限过滤的
    // 设置清单打开期间轻量轮询。出错后停止，而不是反复锤后端。
    refetchInterval: (query) =>
      query.state.status === "success" &&
      query.state.data?.group_discovery_supported === true
        ? 5_000
        : false,
  });

/** 查询某 agent 已绑定的钉钉群（agent 详情页独立缓存与按权限划分的接口）。 */
export const dingtalkAgentGroupsOptions = (wsId: string, agentId: string) =>
  queryOptions({
    queryKey: dingtalkKeys.agentGroups(wsId, agentId),
    queryFn: () => api.listAgentDingTalkGroups(agentId),
    enabled: !!wsId && !!agentId,
    // agent 详情走独立缓存和按权限划分的接口，绝不能借用管理员的工作区清单
    // 再到客户端做过滤。
    refetchInterval: (query) =>
      query.state.status === "success" &&
      query.state.data?.group_discovery_supported === true
        ? 5_000
        : false,
  });

/** 分页查询某安装下「长时间无活跃」的钉钉群。 */
export const dingtalkInactiveGroupsOptions = (wsId: string, installationId: string) =>
  infiniteQueryOptions({
    queryKey: dingtalkKeys.inactiveGroups(wsId, installationId),
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      api.listDingTalkGroups(wsId, {
        activity: "inactive",
        installationId,
        offset: pageParam,
      }),
    getNextPageParam: (page) => page.next_offset,
    enabled: !!wsId && !!installationId,
  });

/** 分页查询某 agent 绑定里「长时间无活跃」的钉钉群。 */
export const dingtalkAgentInactiveGroupsOptions = (
  wsId: string,
  agentId: string,
  installationId: string,
) =>
  infiniteQueryOptions({
    queryKey: dingtalkKeys.agentInactiveGroups(wsId, agentId, installationId),
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      api.listAgentDingTalkGroups(agentId, {
        activity: "inactive",
        installationId,
        offset: pageParam,
      }),
    getNextPageParam: (page) => page.next_offset,
    enabled: !!wsId && !!agentId && !!installationId,
  });
