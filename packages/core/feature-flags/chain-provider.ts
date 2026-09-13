// 本文件实现 ChainProvider：把多个 provider 串成链，按顺序取第一个命中结论。

import type { Decision, EvalContext, Provider } from "./types";

/**
 * ChainProvider 组合多个 provider，并返回第一个命中的结论。
 *
 * 顺序从「最具体」到「最通用」：按请求覆盖、服务端下发、静态配置。
 * 第一个返回 Decision 的 provider 胜出，因此这条链天然实现了调用方期望的
 * 「运维覆盖 > 静态配置」模式。
 *
 * 包装了零个 provider 的 ChainProvider 也是合法的，它永远返回 undefined，
 * 此时 Service 会回退到调用方的默认值。
 */
export class ChainProvider implements Provider {
  readonly name = "chain";
  private readonly providers: ReadonlyArray<Provider>;

  /**
   * @param providers - 按优先级从高到低排列的 provider 列表，可含空项
   */
  constructor(providers: ReadonlyArray<Provider | null | undefined>) {
    // 过滤掉空项，让调用方可以直接传入「可选的」provider：
    // `new ChainProvider([envOverride, baseStatic])`。
    this.providers = providers.filter((p): p is Provider => p != null);
  }

  /** 依次询问链上的每个 provider，返回第一个非 undefined 的结论。 */
  lookup(key: string, ctx: EvalContext): Decision | undefined {
    for (const p of this.providers) {
      const d = p.lookup(key, ctx);
      if (d !== undefined) return d;
    }
    return undefined;
  }
}
