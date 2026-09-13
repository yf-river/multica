// 本文件定义账户级计费（钱包充值/门户）的变更操作，它们会跳转到 Stripe 托管页面。

import { useCallback } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { CreateBillingCheckoutSessionRequest } from "../types";
import { billingKeys } from "./queries";

// 这里两个 mutation 都会触发一次跳出 SPA 的跳转——Stripe Checkout 和 Stripe
// Billing Portal 都是托管页面。一旦 URL 到手，mutation 就算完成；跳转
// （`window.location.href = url`，或新流程里的 `window.open` + 标签页轮询）
// 由调用方负责。
//
// 我们在 settle 时失效充值（topup）列表，这样用户从 Stripe 返回时，新的
// `pending` 订单能立即显示。余额和交易记录不在这里失效——它们要等 Stripe +
// 云端 webhook 真正入账后才变化，那是跳转之后的事。

/**
 * 创建云端计费 Checkout 会话的 mutation。结束后失效充值列表，
 * 让用户无需刷新即可看到新的 pending 充值项。
 */
export function useCreateCloudBillingCheckoutSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateBillingCheckoutSessionRequest) =>
      api.createCloudBillingCheckoutSession(data),
    onSettled: () => {
      // 云端一旦写入新 pending 充值行，充值列表就能看到。
      // 失效以让用户无需刷新页面即可看到新的 pending 项。
      qc.invalidateQueries({ queryKey: [...billingKeys.all(), "topups"] });
    },
  });
}

/**
 * 创建云端计费 Portal 会话的 mutation。
 * 不做缓存失效——门户打开后用户自行操作，Stripe 侧的状态变化通过 webhook 回传，
 * 下一次 React Query 重取会按自己的节奏接住。
 */
export function useCreateCloudBillingPortalSession() {
  return useMutation({
    mutationFn: () => api.createCloudBillingPortalSession(),
  });
}

/**
 * 返回一个回调，用于在充值入账后冲刷缓存的余额 / 交易 / 批次 / 充值数据，
 * 让页面重新拉取。供 Stripe 成功跳转后的轮询横幅使用：一旦它检测到充值状态
 * 翻到了终态（credited / failed / canceled），横幅就是唯一一直在轮询的查询——
 * 页面上其它卡片还停留在结账前的快照。不做这次失效，用户会看到「最终状态：
 * credited」而余额卡片仍显示旧数字，直到手动刷新。
 *
 * 失效范围：
 *
 *   - 余额 + 交易 + 批次：只在 `credited` 转换时变化（云端写账本和批次行时，
 *     与钱包更新在同一 DB 事务里）。对 `failed` / `canceled` 它们不变，所以
 *     严格来说这两种情况下是「多拉取」了——三次便宜的额外往返，简化了调用点，
 *     在测试页面上可忽略不计。
 *
 *   - 充值（topups）：每次终态转换都会变（订单行翻状态），所以总是需要失效。
 *
 *   - checkout-session 查询本身刻意不在这轮失效里。它的 `refetchInterval` 在
 *     状态进入终态时已经返回 `false`；再重取只会确认我们已持有的同一个值，
 *     白白重新唤醒轮询周期。
 */
export function useInvalidateBillingDataAfterCredit() {
  const qc = useQueryClient();
  return useCallback(() => {
    qc.invalidateQueries({ queryKey: billingKeys.balance() });
    qc.invalidateQueries({ queryKey: [...billingKeys.all(), "transactions"] });
    qc.invalidateQueries({ queryKey: [...billingKeys.all(), "batches"] });
    qc.invalidateQueries({ queryKey: [...billingKeys.all(), "topups"] });
  }, [qc]);
}
