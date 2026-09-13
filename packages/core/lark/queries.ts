// 本文件封装飞书（Lark）集成相关的 React Query 查询：安装记录列表。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 所有飞书安装相关查询的 query key 命名空间。实时同步会在收到
 * `lark_installation:*` 事件时失效 `installations(wsId)`，这样设置面板无需
 * 手动刷新就能更新。 */
export const larkKeys = {
  all: (wsId: string) => ["lark", wsId] as const,
  installations: (wsId: string) => [...larkKeys.all(wsId), "installations"] as const,
};

/** 查询工作区下的全部飞书安装记录。 */
export const larkInstallationsOptions = (wsId: string) =>
  queryOptions({
    queryKey: larkKeys.installations(wsId),
    queryFn: () => api.listLarkInstallations(wsId),
    enabled: !!wsId,
  });
