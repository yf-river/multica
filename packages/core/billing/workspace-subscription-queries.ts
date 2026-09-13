// 本文件定义工作区订阅（摘要、价目、issue 限额用量）的查询配置与 key。

import { queryOptions } from "@tanstack/react-query";
import type { WorkspaceSubscriptionPrices } from "../types";
import { api } from "../api";

const WORKSPACE_SUBSCRIPTION_PRICES_STALE_TIME_MS = 10 * 60 * 1000;

/**
 * 工作区订阅读取。
 *
 * 与 `./queries` 里的账户级钱包 key 不同，这些查询按工作区 ID 划分：服务端从请求
 * 上下文解析工作区，所以两个工作区对同一个 URL 会得到不同数据。按工作区划分 key，
 * 正是为了避免切换工作区后，把一个工作区的缓存快照显示成另一个工作区的数据。
 */
export const workspaceSubscriptionKeys = {
  all: (wsId: string) => ["workspace-subscriptions", wsId] as const,
  summary: (wsId: string) =>
    [...workspaceSubscriptionKeys.all(wsId), "summary"] as const,
  prices: (wsId: string) =>
    [...workspaceSubscriptionKeys.all(wsId), "prices"] as const,
  issueLimitUsage: (wsId: string) =>
    [...workspaceSubscriptionKeys.all(wsId), "issue-limit-usage"] as const,
};

/**
 * 计费页的主要读取。短 stale-time 加聚焦时重取：用户在另一个标签页或系统浏览器里
 * 从 Stripe 返回时，无需手动刷新就能看到新状态，且云端在不调 Stripe 的情况下
 * 提供这份数据，重取很便宜。
 */
export function workspaceSubscriptionSummaryOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceSubscriptionKeys.summary(wsId),
    queryFn: () => api.getWorkspaceSubscriptionSummary(),
    enabled: wsId.length > 0,
    staleTime: 30 * 1000,
    refetchOnWindowFocus: true,
  });
}

/** issue 限额用量的查询配置。 */
export function issueLimitUsageOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceSubscriptionKeys.issueLimitUsage(wsId),
    queryFn: () => api.getIssueLimitUsage(),
    enabled: wsId.length > 0,
    staleTime: 30 * 1000,
    refetchOnWindowFocus: true,
  });
}

/**
 * 经过校验的价格快照是稳定的部署配置，但 null 解析兜底不是快照。
 * 让 null 保持新鲜，是为了避免一次瞬时契约/部署失败让价格被压制十分钟、
 * 且计费标签页毫无恢复手段。
 */
export function workspaceSubscriptionPricesStaleTime(
  prices: WorkspaceSubscriptionPrices | null | undefined,
): number {
  return prices ? WORKSPACE_SUBSCRIPTION_PRICES_STALE_TIME_MS : 0;
}

/**
 * 价格是部署配置，不是工作区状态：只有运维重新指向一个 Stripe Price 时才变化。
 * 校验通过的值缓存得比摘要久得多。缺失/畸形的值保持新鲜，这样在后端上线或瞬时
 * 依赖失败后，重新挂载或窗口聚焦即可恢复；云端已缓存 Stripe 读取，所以重新校验
 * 很便宜。
 *
 * 禁用重试，因为这里的失败模式是「配置错误或无法校验的 Price」，立即重试也修不好。
 * 调用方保持 Checkout 可用，并另提供一个显式的「仅重试价格」入口。
 */
export function workspaceSubscriptionPricesOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceSubscriptionKeys.prices(wsId),
    queryFn: () => api.getWorkspaceSubscriptionPrices(),
    enabled: wsId.length > 0,
    staleTime: (query) =>
      workspaceSubscriptionPricesStaleTime(query.state.data),
    refetchOnWindowFocus: true,
    retry: false,
  });
}
