// 本文件封装 Telegram 集成相关的 React Query 查询：安装记录列表。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 所有 Telegram 安装相关查询的 query key 命名空间。实时同步会在收到
 * `telegram_installation:*` 事件时失效 `installations(wsId)`，这样设置面板
 * 无需手动刷新就能更新。 */
export const telegramKeys = {
  all: (wsId: string) => ["telegram", wsId] as const,
  installations: (wsId: string) => [...telegramKeys.all(wsId), "installations"] as const,
};

/** 查询工作区下的全部 Telegram 安装记录。 */
export const telegramInstallationsOptions = (wsId: string) =>
  queryOptions({
    queryKey: telegramKeys.installations(wsId),
    queryFn: () => api.listTelegramInstallations(wsId),
    enabled: !!wsId,
  });
