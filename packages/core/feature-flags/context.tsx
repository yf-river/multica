// 本文件提供 FeatureFlagService 的 React 接线：Provider 把 Service 与求值
// 上下文挂进组件树，Hook 让 UI 代码安全地读取开关结论。

"use client";

import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { EvalContext } from "./types";
import { FeatureFlagService } from "./service";

/**
 * React 层给 FeatureFlagService 提供的粘合代码。对外导出两块：
 *
 *  - {@link FeatureFlagsProvider}：把 Service 和 EvalContext 提供给子树。
 *    Service 通常在应用根节点创建一次；EvalContext 会随用户上下文变化
 *    （例如登录之后）。
 *  - {@link useFlag} / {@link useVariant}：UI 代码里推荐使用的开关读取点。
 *    它们永不抛错；缺 provider 树时回退到传入的默认值，这样 Storybook
 *    故事和单元测试不必为了渲染一个按钮就挂上 provider。
 *
 * 注意：我们刻意不通过 hooks 暴露底层的 FeatureFlagService。需要直接访问
 * 的组件可以读导出的 context 对象，但代价是放弃「永远安全」的兜底保证。
 */

/** Provider 上下文里携带的内容：Service + 本次求值用到的 EvalContext。 */
interface FeatureFlagContextValue {
  service: FeatureFlagService;
  ctx: EvalContext;
}

const FeatureFlagContext = createContext<FeatureFlagContextValue | null>(null);

/** FeatureFlagsProvider 组件的 props。 */
export interface FeatureFlagsProviderProps {
  service: FeatureFlagService;
  /**
   * 该子树内所有开关求值共用的定向上下文。
   * 用户匿名时传空对象即可——此时百分比灰度和白/黑名单会对「空标识」求值，
   * 这正是匿名流量期望的行为。
   */
  context?: EvalContext;
  children: ReactNode;
}

/**
 * 把 FeatureFlagService 与 EvalContext 挂进组件树。
 *
 * 允许（但少见）在重渲染时替换 `service` prop；更推荐在既有 Service 上调用
 * `setProvider` 来改底层 provider，那可以避免强制所有消费方重新求值。
 *
 * @param props - service、可选定向上下文与子树
 */
export function FeatureFlagsProvider({
  service,
  context: ctx = {},
  children,
}: FeatureFlagsProviderProps) {
  const value = useMemo<FeatureFlagContextValue>(
    () => ({ service, ctx }),
    [service, ctx],
  );
  return (
    <FeatureFlagContext.Provider value={value}>{children}</FeatureFlagContext.Provider>
  );
}

/**
 * 读取某个开关的布尔状态。
 *
 * 在 {@link FeatureFlagsProvider} 之外调用时返回 `defaultValue`、永不抛错，
 * 这让测试与故事可以不必依赖 provider。
 *
 * @example
 *   const showNewBilling = useFlag("billing_v2_dashboard", false);
 *   return showNewBilling ? <BillingV2 /> : <BillingV1 />;
 *
 * @param key - 开关名
 * @param defaultValue - 无 provider 时的兜底值
 * @returns 开关是否开启
 */
export function useFlag(key: string, defaultValue: boolean): boolean {
  const value = useContext(FeatureFlagContext);
  if (!value) return defaultValue;
  return value.service.isEnabled(key, value.ctx, defaultValue);
}

/**
 * 读取多臂实验开关的原始变体标识，具备与 {@link useFlag} 相同的「无
 * provider 也安全」的兜底。
 *
 * @example
 *   const variant = useVariant("checkout_algo", "control");
 *   switch (variant) {
 *     case "experiment-v2": return <CheckoutV2 />;
 *     case "experiment-v3": return <CheckoutV3 />;
 *     default:              return <CheckoutControl />;
 *   }
 *
 * @param key - 开关名
 * @param defaultValue - 无 provider 时的兜底变体
 * @returns 变体标识符
 */
export function useVariant(key: string, defaultValue: string): string {
  const value = useContext(FeatureFlagContext);
  if (!value) return defaultValue;
  return value.service.variant(key, value.ctx, defaultValue);
}

/**
 * 给诊断浮层这类需要直接访问 Service 的场景留的「逃生门」。
 * provider 之外返回 `null`，调用方必须显式判空——这是有意的：普通组件代码
 * 应该用 {@link useFlag}，而不是裸 Service。
 *
 * @returns 当前子树里的 FeatureFlagService，缺 provider 时返回 null
 */
export function useFeatureFlagService(): FeatureFlagService | null {
  return useContext(FeatureFlagContext)?.service ?? null;
}
