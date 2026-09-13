// 本文件实现基于内存规则表的 StaticProvider：规则既可以在代码里配置，也可以
// 来自随应用包一起分发的 JSON 配置。

import type { Decision, EvalContext, Provider, Rule } from "./types";
import { inPercent } from "./hash";

/**
 * StaticProvider 是一个内存态 Provider，可由代码填充，也可从随应用包分发的
 * JSON 配置填充。
 *
 * 它是前端推荐的基础 Provider：配置在版本控制里、随构建走 CD、改动需要重新
 * 发布。对于从后端拉取的动态开关，可以在链式 provider 里把一个
 * {@link StaticProvider} 放在同样读 API 状态的 provider 之后——此时
 * StaticProvider 充当「第一帧」的安全网：在 API 响应回来之前兜住默认行为。
 */
export class StaticProvider implements Provider {
  readonly name = "static";
  private rules: Map<string, Rule>;

  /**
   * @param rules - 初始的「开关名 → 规则」映射，可省略
   */
  constructor(rules: Readonly<Record<string, Rule>> = {}) {
    this.rules = new Map(Object.entries(rules));
  }

  /** 替换或新装 `key` 对应的规则。 */
  set(key: string, rule: Rule): void {
    this.rules.set(key, rule);
  }

  /**
   * 原子地替换全部规则。从 fetch 响应重新加载开关配置时用，避免消费方观察到
   * 新旧混杂的中间状态。
   *
   * @param rules - 新的「开关名 → 规则」映射
   */
  loadRules(rules: Readonly<Record<string, Rule>>): void {
    this.rules = new Map(Object.entries(rules));
  }

  /** 返回已知开关名排序后的列表，开发浮层用。 */
  keys(): string[] {
    return Array.from(this.rules.keys()).sort();
  }

  /** 求值单个开关；本 provider 不认识该开关时返回 undefined（交给链上后续 provider）。 */
  lookup(key: string, ctx: EvalContext): Decision | undefined {
    const rule = this.rules.get(key);
    if (!rule) return undefined;
    return evaluateRule(key, rule, ctx);
  }
}

/**
 * 按 Rule 的求值顺序（deny → allow → percent → default）求值一个开关。
 *
 * @param key - 开关名
 * @param rule - 该开关的规则
 * @param ctx - 求值上下文
 * @returns 求值结论
 */
function evaluateRule(key: string, rule: Rule, ctx: EvalContext): Decision {
  // deny 优先于一切：总开关必须即使在其他定向规则命中时也能生效（kill switch）。
  const denyBy = rule.denyBy ?? "user_id";
  if (rule.deny && rule.deny.length > 0) {
    const v = lookupAttr(ctx, denyBy);
    if (v && rule.deny.includes(v)) {
      return decisionFromRule(key, rule, false, "static");
    }
  }

  const allowBy = rule.allowBy ?? "user_id";
  if (rule.allow && rule.allow.length > 0) {
    const v = lookupAttr(ctx, allowBy);
    if (v && rule.allow.includes(v)) {
      return decisionFromRule(key, rule, true, "static");
    }
  }

  if (rule.percent) {
    const by = rule.percent.by ?? "user_id";
    const ident = lookupAttr(ctx, by) ?? "";
    const enabled = inPercent(key, ident, rule.percent.percent);
    return decisionFromRule(key, rule, enabled, "percent");
  }

  return decisionFromRule(key, rule, rule.default ?? false, "static");
}

/**
 * 把规则求值结果包装成标准 Decision。
 *
 * @param key - 开关名
 * @param rule - 该开关的规则（取 variant 用）
 * @param enabled - 规则算出的开关状态
 * @param reason - 结论原因
 * @returns 标准 Decision
 */
function decisionFromRule(
  key: string,
  rule: Rule,
  enabled: boolean,
  reason: Decision["reason"],
): Decision {
  // variant 策略：rule.variant 是「开」时的变体。当规则判为关时返回规范值
  // "off"，这样按变体分支的调用方不可能因为用户没灰度进来而误入实验组。
  let variant = boolToVariant(enabled);
  if (enabled && rule.variant && rule.variant.length > 0) {
    variant = rule.variant;
  }
  return {
    key,
    enabled,
    variant,
    reason,
    source: "static",
  };
}

/** 把布尔开关状态转成规范变体字符串："on" / "off"。 */
function boolToVariant(b: boolean): string {
  return b ? "on" : "off";
}

/**
 * 按属性名从 EvalContext 解析取值。"user_id" 和 "workspace_id" 这两个已知名会
 * 映射到专用字段，这样规则按名引用它们时，调用方无需同时往 `attributes` 里填。
 *
 * @param ctx - 求值上下文
 * @param name - 属性名
 * @returns 非空字符串值，缺失或为空返回 undefined
 */
function lookupAttr(ctx: EvalContext, name: string): string | undefined {
  if (name === "user_id") return nonEmpty(ctx.userId);
  if (name === "workspace_id") return nonEmpty(ctx.workspaceId);
  return nonEmpty(ctx.attributes?.[name]);
}

/** 把空字符串也归一成 undefined，方便上层做「有没有值」的判断。 */
function nonEmpty(v: string | undefined): string | undefined {
  return v && v.length > 0 ? v : undefined;
}
