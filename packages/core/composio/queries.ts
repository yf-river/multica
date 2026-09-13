// 本文件定义 Composio 集成（工具包与连接）的查询配置与 key。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** Composio 集成数据的查询 key 命名空间。 */
export const composioKeys = {
  all: ["composio"] as const,
  toolkits: () => [...composioKeys.all, "toolkits"] as const,
  connections: () => [...composioKeys.all, "connections"] as const,
};

/** 项目可连接的 Composio 工具包（启用 auth 配置的那些；见 MUL-4009）。
 * 列表很少变化，所以用较长的 staleTime 避免每次 Settings 标签页挂载都重取。 */
export const composioToolkitsOptions = () =>
  queryOptions({
    queryKey: composioKeys.toolkits(),
    queryFn: () => api.listComposioToolkits(),
    staleTime: 5 * 60 * 1000,
  });

/** 当前用户活跃的 Composio 连接。 */
export const composioConnectionsOptions = () =>
  queryOptions({
    queryKey: composioKeys.connections(),
    queryFn: () => api.listComposioConnections(),
  });
