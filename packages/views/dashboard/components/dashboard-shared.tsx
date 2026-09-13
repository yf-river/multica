"use client";

// 仪表盘共享组件：时间范围常量、维度（日/周）切换控件、时长数字组件等，
// 被 dashboard-page 和各卡片复用。
import {
  NumberFlow,
  NumberFlowGroup,
} from "@multica/ui/components/ui/number-flow";
import { formatDuration } from "../utils";

/** 时间段选择项，和运行时详情页保持一致，这样两个仪表盘看到的选项是一样的。
 *  `dims` 声明每个时间段允许按哪种图表维度（日/周）来画：1d/7d 若画成周粒度
 *  会塌缩成一根柱子，180d 若画成日粒度则是 180 根没法看的柱子。
 *
 *  1d 的语义是「今天」（按查看者时区从 00:00 起的自然日历日），而不是「最近 24 小时」。
 *  页面上的 `dailyCutoffIso` 过滤即使在午夜边缘也会守住这个语义。 */
export const TIME_RANGES = [
  { label: "1d", days: 1, dims: ["daily"] as const },
  { label: "7d", days: 7, dims: ["daily"] as const },
  { label: "30d", days: 30, dims: ["daily", "weekly"] as const },
  { label: "90d", days: 90, dims: ["daily", "weekly"] as const },
  { label: "180d", days: 180, dims: ["weekly"] as const },
] as const;

/** 时间段选项，取值为该时间段的天数。 */
export type TimeRange = (typeof TIME_RANGES)[number]["days"];
/** 图表维度：按日聚合还是按周聚合。 */
export type Dim = "daily" | "weekly";

/**
 * 返回当前时间段允许使用的图表维度列表。
 *
 * 以前「时间段」和「维度」这层约束是双向的：在页面头部放两个控件，1d 时选了「周」
 * 会悄悄把时间段重置成 90d，从而带动页面上每个 KPI。现在时间段是页面级筛选、
 * 维度是卡片级控件，依赖只单向存在——卡片只提供它的时间段允许的维度，什么都不用重置。
 * 卡片级控件绝不能反过来去改页面级控件（MUL-5759）。
 *
 * @param days 当前选中的时间段天数
 * @returns 该时间段允许的维度数组（找不到时兜底返回 ["daily"]）
 */
export function dimsForDays(days: TimeRange): readonly Dim[] {
  return (
    TIME_RANGES.find((r) => r.days === days)?.dims ?? (["daily"] as const)
  );
}

/** 表示「未按项目筛选」的哨兵值。特意和空字符串区分开，这样即使将来重构
 *  允许用 slug 作为项目键，这个值也不会被撞上。 */
export const ALL_PROJECTS = "__all__";

/**
 * 卡片级分段切换控件——放在卡片内部用来选「这张卡显示什么」的胶囊按钮。
 * 页面头部刻意用 outline 按钮而不是它，这样靠控件形状就能看出作用范围：
 * 胶囊改的是单张卡片，按钮改的是整个页面。
 *
 * 没用 shadcn 的 Tabs 是因为 Tabs 面向整页标签、带一堆这套紧凑工具栏胶囊用不上的
 * ARIA 语义。以前「哪个选项选中」只靠颜色区分，屏幕阅读器根本看不到，所以现在用
 * `aria-pressed` 一并表达。`label` 是必填而不是可选，因为一组光秃秃的切换按钮被读出来时
 * 不会说明它在切换什么——在不知道这组是「失败者排行」之前，「Rate, 已按下」毫无意义。
 * 用切换按钮而不是 radiogroup：radiogroup 需要支持方向键焦点漫游，而它无论出现在
 * 页面哪里都只是普通 tab 停靠点。
 *
 * @param value 当前选中的值
 * @param onChange 选项变化时的回调
 * @param options 可选项列表（label 为显示文案，value 为实际值）
 * @param label 这组按钮的无障碍名称，说明它在切换什么
 */
export function Segmented<T extends string | number>({
  value,
  onChange,
  options,
  label,
}: {
  value: T;
  onChange: (v: T) => void;
  options: readonly { label: string; value: T }[];
  label: string;
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className="inline-flex items-center gap-0.5 rounded-md bg-muted p-0.5"
    >
      {options.map((o) => (
        <button
          key={String(o.value)}
          type="button"
          aria-pressed={o.value === value}
          onClick={() => onChange(o.value)}
          className={`rounded-sm px-2.5 py-1 text-caption font-medium transition-colors ${
            o.value === value
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground"
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

/**
 * 带滚动动画的时长显示组件。
 *
 * 把秒数格式化（如 "1h 23m"）后，拆成数字和单位两段交给 NumberFlow 做滚动动画，
 * 同时输出一份 sr-only 的完整文案给屏幕阅读器。
 *
 * @param seconds 时长（秒）
 * @param lessThanMinuteLabel 不足一分钟时显示的文案
 * @param locales 用于数字格式化的 locale
 */
export function DurationNumberFlow({
  seconds,
  lessThanMinuteLabel,
  locales,
}: {
  seconds: number;
  lessThanMinuteLabel: string;
  locales?: Intl.LocalesArgument;
}) {
  const label = formatDuration(seconds, lessThanMinuteLabel);
  const parts = Array.from(label.matchAll(/(\d+)([a-z]+)/gi), (match) => ({
    value: Number(match[1]),
    unit: match[2] ?? "",
  }));

  if (parts.length === 0) return label;

  return (
    <>
      <span className="sr-only">{label}</span>
      <NumberFlowGroup>
        <span aria-hidden className="inline-flex items-baseline gap-1">
          {parts.map((part) => (
            <NumberFlow
              key={part.unit}
              value={part.value}
              locales={locales}
              suffix={part.unit}
              format={{ maximumFractionDigits: 0, useGrouping: false }}
            />
          ))}
        </span>
      </NumberFlowGroup>
    </>
  );
}
