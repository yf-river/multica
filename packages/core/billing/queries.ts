// 本文件定义账户级计费（余额、交易、批次、充值、价目）的查询配置与 key。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

// 计费数据是账户级的（每个 X-User-ID 一个所有者），因此 React Query key 不按
// 工作区划分——按工作区划分会让用户每切换一次工作区就强制重取，尽管底层数据
// 完全一样。
//
// 查询级的保鲜度：云端的计费模块是事实来源。充值状态在 Stripe + 云端 webhook
// 处理器完成后，会从 `pending` → `paid` → `credited` 翻转，所以从 Stripe 返回
// 的页面需要及时的新鲜度。我们依赖页面级 hook（refetchInterval / 挂载时失效），
// 而不是在这里写死 stale-time，这样消费者可以按界面调轮询节奏。

/** 账户级计费查询的 key 工厂。 */
export const billingKeys = {
  all: () => ["billing"] as const,
  balance: () => [...billingKeys.all(), "balance"] as const,
  transactions: (params?: { page?: number; page_size?: number }) =>
    [...billingKeys.all(), "transactions", params ?? {}] as const,
  batches: (params?: { page?: number; page_size?: number }) =>
    [...billingKeys.all(), "batches", params ?? {}] as const,
  topups: (params?: { page?: number; page_size?: number }) =>
    [...billingKeys.all(), "topups", params ?? {}] as const,
  priceTiers: () => [...billingKeys.all(), "price-tiers"] as const,
  checkoutSession: (sessionId: string) =>
    [...billingKeys.all(), "checkout-session", sessionId] as const,
};

/**
 * 账户余额的查询配置。
 * 30 秒 stale-time：余额只在充值入账或扣款时变化。测试页里用户主要关心
 * 结账后的状态；我们让页面显式失效。
 */
export function billingBalanceOptions() {
  return queryOptions({
    queryKey: billingKeys.balance(),
    queryFn: () => api.getCloudBillingBalance(),
    staleTime: 30 * 1000,
  });
}

/** 交易记录的查询配置。 */
export function billingTransactionsOptions(params?: {
  page?: number;
  page_size?: number;
}) {
  return queryOptions({
    queryKey: billingKeys.transactions(params),
    queryFn: () => api.listCloudBillingTransactions(params),
    staleTime: 30 * 1000,
  });
}

/** 批次的查询配置。 */
export function billingBatchesOptions(params?: {
  page?: number;
  page_size?: number;
}) {
  return queryOptions({
    queryKey: billingKeys.batches(params),
    queryFn: () => api.listCloudBillingBatches(params),
    staleTime: 30 * 1000,
  });
}

/** 充值记录的查询配置。 */
export function billingTopupsOptions(params?: {
  page?: number;
  page_size?: number;
}) {
  return queryOptions({
    queryKey: billingKeys.topups(params),
    queryFn: () => api.listCloudBillingTopups(params),
    staleTime: 30 * 1000,
  });
}

/**
 * 价目（price tiers）的查询配置。
 * 价目来自服务端配置，运行时基本不会变——拉取一次即可保留整个会话。
 * 5 分钟对测试页来说绰绰有余。
 */
export function billingPriceTiersOptions() {
  return queryOptions({
    queryKey: billingKeys.priceTiers(),
    queryFn: () => api.listCloudBillingPriceTiers(),
    staleTime: 5 * 60 * 1000,
  });
}

// Stripe 成功跳转后的轮询：页面带着 `?session_id=...` 加载时，用户刚从 Stripe
// 返回，充值正在 `pending → paid → credited` 快速推进。一直轮询到终态，让 UI 在
// 用户关闭标签页前能展示最终结果。
//
// 调用方应通过传 `enabled: !!sessionId` 短路——这就是为什么不在 queryOptions
// 内部做门控。
/**
 * Checkout 会话的轮询查询配置。非终态时每 2 秒重取，进入 credited / failed /
 * canceled 后停止轮询。
 */
export function billingCheckoutSessionOptions(sessionId: string) {
  return queryOptions({
    queryKey: billingKeys.checkoutSession(sessionId),
    queryFn: () => api.getCloudBillingCheckoutSession(sessionId),
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      if (status === "credited" || status === "failed" || status === "canceled") {
        return false;
      }
      return 2000;
    },
    staleTime: 0,
  });
}
