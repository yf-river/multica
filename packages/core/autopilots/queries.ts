// 本文件定义自动驾驶（autopilot）相关的查询（query options）与查询 key。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 自动驾驶相关查询的 key 工厂，统一维护以保证缓存键一致。 */
export const autopilotKeys = {
  all: (wsId: string) => ["autopilots", wsId] as const,
  list: (wsId: string) => [...autopilotKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...autopilotKeys.all(wsId), "detail", id] as const,
  runs: (wsId: string, id: string) =>
    [...autopilotKeys.all(wsId), "runs", id] as const,
  run: (wsId: string, autopilotId: string, runId: string) =>
    [...autopilotKeys.all(wsId), "runs", autopilotId, runId] as const,
  deliveries: (wsId: string, id: string) =>
    [...autopilotKeys.all(wsId), "deliveries", id] as const,
  delivery: (wsId: string, autopilotId: string, deliveryId: string) =>
    [...autopilotKeys.all(wsId), "deliveries", autopilotId, deliveryId] as const,
  cronPreview: (wsId: string, expr: string, tz: string) =>
    [...autopilotKeys.all(wsId), "cron-preview", expr, tz] as const,
};

/** 自动驾驶列表的查询配置，select 只取列表里的 autopilots 数组。 */
export function autopilotListOptions(wsId: string) {
  return queryOptions({
    queryKey: autopilotKeys.list(wsId),
    queryFn: () => api.listAutopilots(),
    select: (data) => data.autopilots,
  });
}

/** 单个自动驾驶详情的查询配置。 */
export function autopilotDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: autopilotKeys.detail(wsId, id),
    queryFn: () => api.getAutopilot(id),
  });
}

/** 自动驾驶运行列表的查询配置，select 只取 runs 数组。 */
export function autopilotRunsOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: autopilotKeys.runs(wsId, id),
    queryFn: () => api.listAutopilotRuns(id),
    select: (data) => data.runs,
  });
}

// autopilotRunOptions 拉取单次运行的完整 trigger_payload。
// 列表端点（autopilotRunsOptions）为了保持响应轻量而省略 trigger_payload；
// 调用方（例如运行详情弹窗）在用户打开某次运行时按需使用本查询。
/**
 * 单次运行详情的查询配置，携带完整的 trigger_payload。
 */
export function autopilotRunOptions(
  wsId: string,
  autopilotId: string,
  runId: string,
  options?: { enabled?: boolean },
) {
  return queryOptions({
    queryKey: autopilotKeys.run(wsId, autopilotId, runId),
    queryFn: () => api.getAutopilotRun(autopilotId, runId),
    enabled: options?.enabled ?? true,
  });
}

// autopilotDeliveriesOptions 支撑自动驾驶详情页里的「投递」区块。列表是精简的——
// raw_body / selected_headers / response_body 在服务端被省略。展开某行时才按需拉取
// 详情（见 autopilotDeliveryOptions）。
/**
 * 自动驾驶投递列表的查询配置（精简字段）。
 */
export function autopilotDeliveriesOptions(
  wsId: string,
  autopilotId: string,
  options?: { enabled?: boolean },
) {
  return queryOptions({
    queryKey: autopilotKeys.deliveries(wsId, autopilotId),
    queryFn: () => api.listAutopilotDeliveries(autopilotId),
    select: (data) => data.deliveries,
    enabled: options?.enabled ?? true,
  });
}

// autopilotDeliveryOptions 拉取完整的投递记录，包括 raw_body 和 headers 子集。
// 供从列表行打开的详情弹窗使用。
/**
 * 单条投递详情的查询配置（含完整 raw_body 与 headers 子集）。
 */
export function autopilotDeliveryOptions(
  wsId: string,
  autopilotId: string,
  deliveryId: string,
  options?: { enabled?: boolean },
) {
  return queryOptions({
    queryKey: autopilotKeys.delivery(wsId, autopilotId, deliveryId),
    queryFn: () => api.getAutopilotDelivery(autopilotId, deliveryId),
    enabled: options?.enabled ?? true,
  });
}

// cronPreviewOptions 支撑调度编辑器里的「下次运行」预览。服务端负责 cron/时区求值，
// 所以编辑器从不在本地近似计算。
/**
 * cron 表达式下次运行时间预览的查询配置。表达式非法时返回 400 是稳定答案而非
 * 瞬时故障，故不重试，避免拖慢行内错误提示。
 */
export function cronPreviewOptions(
  wsId: string,
  expr: string,
  tz: string,
  options?: { enabled?: boolean },
) {
  return queryOptions({
    queryKey: autopilotKeys.cronPreview(wsId, expr, tz),
    queryFn: () => api.cronPreview({ expr, tz }),
    enabled: options?.enabled ?? true,
    staleTime: 30_000,
    retry: false,
  });
}
