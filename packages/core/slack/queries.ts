// 本文件封装 Slack 集成相关的 React Query 查询：安装记录列表。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 所有 Slack 安装相关查询的 query key 命名空间。实时同步会在收到
 * `slack_installation:*` 事件时失效 `installations(wsId)`，这样设置面板无需
 * 手动刷新就能更新（例如 OAuth 回调在另一个标签页/系统浏览器里完成安装后）。 */
export const slackKeys = {
  all: (wsId: string) => ["slack", wsId] as const,
  installations: (wsId: string) => [...slackKeys.all(wsId), "installations"] as const,
};

/** 查询工作区下的全部 Slack 安装记录。 */
export const slackInstallationsOptions = (wsId: string) =>
  queryOptions({
    queryKey: slackKeys.installations(wsId),
    queryFn: () => api.listSlackInstallations(wsId),
    enabled: !!wsId,
  });
