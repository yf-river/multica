// 本文件封装 VCS（Git 提供方，如 Forgejo/Gitea/GitLab）连接的 React Query
// 查询。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** VCS 相关查询的 query key 命名空间。 */
export const vcsKeys = {
  all: (wsId: string) => ["vcs", wsId] as const,
  connections: (wsId: string) => [...vcsKeys.all(wsId), "connections"] as const,
};

/** 查询工作区下的 VCS 连接列表。 */
export const vcsConnectionsOptions = (wsId: string) =>
  queryOptions({
    queryKey: vcsKeys.connections(wsId),
    queryFn: () => api.listVCSConnections(wsId),
    enabled: !!wsId,
  });
