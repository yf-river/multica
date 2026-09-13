// 本文件定义 @multica/core/feature-flags 模块的对外类型。
//
// 这些结构刻意与 Go 侧 server/pkg/featureflag 保持一致：后端返回的 Decision
// 可以不经过任何转换直接序列化给前端 Service 使用。两侧扩展时请保持同步。

/**
 * Reason 解释一次 Decision 为什么得到这个结果。它会出现在诊断接口和开发
 * 浮层里，让工程师能区分「这个开关开着是因为用户在白名单里」和「这个开关
 * 开着是因为默认值生效了」。
 */
export type Reason =
  | "static"
  | "percent"
  | "override"
  | "default"
  | "error";

/**
 * 单个开关的一次求值结果。大多数调用方只需要 {@link FeatureFlagService.isEnabled}
 * 这个便捷方法，但测试和开发工具想要完整记录。
 */
export interface Decision {
  /** 被求值的开关标识。 */
  key: string;
  /** 布尔投影：除了 "off" / "" / "false" / "0" 之外的任意 variant 都为 true。 */
  enabled: boolean;
  /** 原始变体值。布尔开关用 "on" / "off"；变体开关用任意标识符。 */
  variant: string;
  /** 这次结论产生的原因。 */
  reason: Reason;
  /** 产出该结论的 provider 名；什么都没命中时为 "default"。 */
  source: string;
}

/**
 * 每次求值携带的动态定向上下文（白/黑名单、百分比灰度）。所有字段可选：
 * 缺少某个字段不会让求值崩溃，只会跳过依赖它的规则。
 */
export interface EvalContext {
  userId?: string;
  workspaceId?: string;
  /** 自由格式的属性（套餐、国家、客户端……）。key 区分大小写。 */
  attributes?: Readonly<Record<string, string>>;
}

/**
 * 百分比灰度描述。(key, identifier) 用 FNV-1a 算出桶号，因此同一个标识符在
 * 不同进程、不同标签页里总是落进同一个桶。
 */
export interface PercentRollout {
  /** 灰度比例，范围 [0, 100]，越界会被夹回边界。 */
  percent: number;
  /**
   * 用作分桶标识的属性名，默认 "user_id"。工作区维度灰度用 "workspace_id"。
   */
  by?: string;
}

/**
 * Rule 描述 {@link StaticProvider} 如何求值单个开关。
 *
 * 求值顺序（先命中先赢）：
 *   1. Deny：若 EvalContext 的属性命中 deny 里的条目，返回 OFF。
 *   2. Allow：若命中 allow 里的条目，返回 ON。
 *   3. Percent：若桶号落在 percent.percent 内，返回 ON，否则 OFF。
 *   4. Default：返回 defaultValue。
 */
export interface Rule {
  /** 没有任何定向规则命中时返回的值，默认 false。 */
  default?: boolean;
  /**
   * 当规则判定 enabled=true 时返回的变体标识，用于多臂实验（例如
   * "experiment-v2"）。当规则判定 enabled=false 时，Decision 的 variant
   * 恒为 "off"，这样按 `Variant()` 分支的调用方不可能因为用户没灰度进来
   * 而误入实验组。
   */
  variant?: string;
  /** 命中就强制把开关打开（ON）的标识符列表。 */
  allow?: ReadonlyArray<string>;
  /** allow 查询所用的 EvalContext 属性名，默认 "user_id"。 */
  allowBy?: string;
  /** 命中就强制把开关关掉（OFF）的标识符列表。Deny 优先于 allow。 */
  deny?: ReadonlyArray<string>;
  /** deny 查询所用的 EvalContext 属性名，默认 "user_id"。 */
  denyBy?: string;
  /** 确定性的百分比灰度。 */
  percent?: PercentRollout;
}

/**
 * Provider 是 Service 的配置来源。实现 MUST 能并发安全使用：Service 会被很多
 * 组件同时读取，而不额外做同步。
 *
 * 返回 `undefined`（而不是一个 Decision）等于告诉 Service：让 ChainProvider
 * 里下一个 provider 继续，或在没有下一个 provider 时回退到调用方给的默认值。
 */
export interface Provider {
  /** 稳定的、人类可读的标识，会体现在 Decision.source 里。 */
  readonly name: string;
  /** 求值一个开关；本 provider 不认识这个开关时返回 undefined。 */
  lookup(key: string, ctx: EvalContext): Decision | undefined;
}
