// 本文件封装企业微信集成相关的 React Query 查询：安装记录列表。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 所有企业微信安装相关查询的 query key 命名空间。实时同步会在收到
 * `wecom_installation:*` 事件时失效 `installations(wsId)`，这样另一个标签页
 * 或另一台机器上的管理员连接/断开机器人时，设置面板无需手动刷新。 */
export const wecomKeys = {
  all: (wsId: string) => ["wecom", wsId] as const,
  installations: (wsId: string) => [...wecomKeys.all(wsId), "installations"] as const,
};

/** 查询工作区下的全部企业微信安装记录。 */
export const wecomInstallationsOptions = (wsId: string) =>
  queryOptions({
    queryKey: wecomKeys.installations(wsId),
    queryFn: () => api.listWecomInstallations(wsId),
    enabled: !!wsId,
  });
