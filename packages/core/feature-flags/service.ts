// 本文件实现 feature-flags 的框架级「开关路由器」：UI 代码向 Service 要结论，
// Service 去问它配置的 {@link Provider}。

import type { Decision, EvalContext, Provider } from "./types";

/**
 * FeatureFlagService 是框架级的开关路由中枢。UI 代码向它询问开关结论，
 * 由它去查询配置好的 {@link Provider}。
 *
 * 该类刻意做成无副作用：挂进 React 组件树的工作由 `./context.tsx` 负责，
 * Service 本身在 React 之外也能用（单元测试、web worker、Node CLI……）。
 *
 * 默认安全的策略：当没有任何 provider 命中时，所有对外入口都返回调用方给的
 * 默认值。业务代码永远不需要自己去兜「开关不存在」的情况。
 */
export class FeatureFlagService {
  private provider: Provider | null;

  /**
   * @param provider - 初始的开关 provider，可为空
   */
  constructor(provider: Provider | null = null) {
    this.provider = provider;
  }

  /**
   * 运行时替换底层 provider。适合后端下发新配置时用；React 的 provider 树
   * 会因消费它的 hook 订阅了包装层而自动重渲染。
   *
   * @param provider - 新的 provider，传 null 表示清空
   */
  setProvider(provider: Provider | null): void {
    this.provider = provider;
  }

  /**
   * 判断命名开关当前是否为「开」。开关未知时返回调用方给的默认值。
   *
   * @example
   *   if (flags.isEnabled("billing_new_invoice_email", { userId }, false)) {
   *     return <NewInvoiceEmail />;
   *   }
   *   return <LegacyInvoiceEmail />;
   *
   * @param key - 开关名
   * @param ctx - 求值上下文（定向规则用）
   * @param defaultValue - 无 provider 命中时返回的默认值
   * @returns 开关是否开启
   */
  isEnabled(key: string, ctx: EvalContext, defaultValue: boolean): boolean {
    return this.decision(key, ctx, defaultValue).enabled;
  }

  /**
   * 返回多臂实验开关的原始变体；没有任何匹配时回退到 `defaultValue`。
   *
   * @param key - 开关名
   * @param ctx - 求值上下文（定向规则用）
   * @param defaultValue - 无 provider 命中时返回的默认变体
   * @returns 变体标识符
   */
  variant(key: string, ctx: EvalContext, defaultValue: string): string {
    if (!this.provider) {
      return defaultValue;
    }
    const d = this.provider.lookup(key, ctx);
    if (!d) return defaultValue;
    return d.variant;
  }

  /**
   * 返回完整结构的求值结论，供诊断浮层和测试使用。
   *
   * @param key - 开关名
   * @param ctx - 求值上下文（定向规则用）
   * @param defaultValue - 无 provider 命中时的默认开关状态
   * @returns 完整的 Decision 结构
   */
  decision(key: string, ctx: EvalContext, defaultValue: boolean): Decision {
    if (!this.provider) {
      return defaultDecision(key, defaultValue);
    }
    const d = this.provider.lookup(key, ctx);
    if (!d) return defaultDecision(key, defaultValue);
    return { ...d, key };
  }

  /** 返回当前包装的 provider（只读），供诊断使用。 */
  getProvider(): Provider | null {
    return this.provider;
  }
}

/** 构造一个「默认值生效」的 Decision，用于没有任何 provider 命中的情况。 */
function defaultDecision(key: string, value: boolean): Decision {
  return {
    key,
    enabled: value,
    variant: value ? "on" : "off",
    reason: "default",
    source: "default",
  };
}
