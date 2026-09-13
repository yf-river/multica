// ============================================================================
// 失败分类（failure class）的图表视觉工具
//
// 角色定位：把「一次运行为什么失败」按七类（鉴权、限流、超时、厂商、运行时、
// agent、其他）归因的计数，翻译成错误堆叠图/悬浮提示需要的颜色、标题与标签。
// 纯视觉逻辑，不含任何图表组件；错误图组件（daily-errors-chart、
// weekly-errors-chart）从这里取配置与工具函数。
//
// 数据来源：失败类别 id 由 @multica/core/dashboard 的 FAILURE_CLASSES 定义，
// 后端每桶（每天/每周）按类别计数上报，本文件负责把它们变成可以画的序列。
// ============================================================================
import { FAILURE_CLASSES, type FailureClass } from "@multica/core/dashboard";
import type { ChartConfig } from "@multica/ui/components/ui/chart";
import { useT } from "../../../i18n";

// 设计系统只给了五个 `--chart-*` 颜色 token，且是同一蓝色渐变——没有七色
// 分类色板可借用；而借用蓝色会让「失败」看起来像「token 花费」。于是每个
// 类别都是往 `--card` 方向掺入不同比例的 `--destructive`（红色）做成的递减
// 色阶：整根堆叠一眼就能读出「全是错误」，各段又彼此可分。往 `--card` 掺
// 而不是固定掺白色，暗色模式下色阶依然清晰可辨。
//
// 色阶底部停在 30% 而不是淡到看不见，保证最不突出的类别在卡片背景上也仍然
// 可见。顺序与 FAILURE_CLASSES 一致（最值得处理的最靠前），所以最深的那段
// 永远是应该先看的那一类。

// 一个桶（一天/一周）内按展示类别计的失败数。每个类别都恒存在（没用到时为
// 0），读数据时无需判空；真正画哪几类是 activeFailureClasses 的决定。
export type FailureClassCounts = Record<FailureClass, number>;

// 随各类计数一起传递的桶级汇总。`total` 数的是桶内所有「终态任务」而非仅
// 失败任务，好让悬浮提示能给出失败率。
export interface FailureBucketTotals {
  failed: number;
  total: number;
}

// 七类的堆叠配色：`auth` 用纯红，其余逐档掺入卡片底色以递减，构成同一根
// 色阶。见文件头导读对配色动机的解释。
export const FAILURE_CLASS_COLOR: Record<FailureClass, string> = {
  auth: "var(--destructive)",
  rate_limit: "color-mix(in oklch, var(--destructive) 86%, var(--card))",
  timeout: "color-mix(in oklch, var(--destructive) 72%, var(--card))",
  provider: "color-mix(in oklch, var(--destructive) 60%, var(--card))",
  runtime: "color-mix(in oklch, var(--destructive) 48%, var(--card))",
  agent: "color-mix(in oklch, var(--destructive) 38%, var(--card))",
  other: "color-mix(in oklch, var(--destructive) 30%, var(--card))",
};

/**
 * 返回窗口数据里实际有失败的类别，按规范顺序排列。
 *
 * 无条件把七类全画出来，会让每个悬浮提示里出现五条恒为 0 的序列。只保留
 * 窗口内真正出现的类别，图例与提示就与实际发生的情况成比例；保留规范顺序
 * 则保证某个类别无论相邻类别是否存在，始终固定在同一个颜色上。
 */
export function activeFailureClasses(
  data: readonly Partial<Record<FailureClass, number>>[],
): FailureClass[] {
  return FAILURE_CLASSES.filter((c) => data.some((d) => (d[c] ?? 0) > 0));
}

/**
 * 失败率——失败数占终态任务的百分比，文案形如 "12.5%"、"40%"。
 *
 * 低于 10% 的比率保留一位小数，因为回落（regression）往往先在这个区间
 * 显形，把 1.4% 四舍五入成 1% 会掩盖一半的波动。没有终态任务的桶谈不上
 * 失败率，渲染成破折号而非「0%」——0% 会被误读成「健康」。
 */
export function formatRate(failed: number, total: number): string {
  if (total <= 0) return "—";
  const pct = (failed / total) * 100;
  return `${pct >= 10 || pct === 0 ? Math.round(pct) : pct.toFixed(1)}%`;
}

/**
 * 把 Recharts 的序列名翻译成已本地化的标签。
 *
 * Recharts 会把 dataKey 原样作为悬浮提示条目的 `name` 传进来，如果格式化
 * 函数直接回显它，用户看到的是裸类别 id（"rate_limit"）。改为到驱动图例的
 * 同一份 config 里查标签；万一 config 丢了某个条目，就回退显示键本身。
 */
export function labelOf(
  config: ChartConfig,
  name: string | number | undefined,
): string {
  const key = String(name ?? "");
  const label = config[key]?.label;
  return typeof label === "string" ? label : key;
}

/**
 * 失败类别序列的「已翻译标签 + 配色」Recharts 配置（图例与提示共用）。
 */
export function useFailureClassConfig(): ChartConfig {
  const { t } = useT("usage");
  return {
    auth: { label: t(($) => $.errors.class.auth), color: FAILURE_CLASS_COLOR.auth },
    rate_limit: {
      label: t(($) => $.errors.class.rate_limit),
      color: FAILURE_CLASS_COLOR.rate_limit,
    },
    timeout: {
      label: t(($) => $.errors.class.timeout),
      color: FAILURE_CLASS_COLOR.timeout,
    },
    provider: {
      label: t(($) => $.errors.class.provider),
      color: FAILURE_CLASS_COLOR.provider,
    },
    runtime: {
      label: t(($) => $.errors.class.runtime),
      color: FAILURE_CLASS_COLOR.runtime,
    },
    agent: { label: t(($) => $.errors.class.agent), color: FAILURE_CLASS_COLOR.agent },
    other: { label: t(($) => $.errors.class.other), color: FAILURE_CLASS_COLOR.other },
  } satisfies ChartConfig;
}
