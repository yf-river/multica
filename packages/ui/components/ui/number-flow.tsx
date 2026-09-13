"use client"
// 本文件是数字滚动动画组件族（基于 @number-flow/react）：
// 数字变化时旧数字滚动过渡到新数字。提供通用 NumberFlow 与三种常用场景
// 的预置形态：货币（Currency）、紧凑缩写（Compact，1.2K/3.4M）、上限封顶
//（Capped，99+）。

import NumberFlowPrimitive, {
  NumberFlowGroup,
  type NumberFlowProps,
} from "@number-flow/react"

import { cn } from "../../lib/utils"

/**
 * NumberFlow：通用数字滚动组件。开了等宽数字避免跳动。
 * isolate 隔离重排、respectMotionPreference 尊重系统「减少动态」偏好。
 */
function NumberFlow({
  className,
  isolate = true,
  respectMotionPreference = true,
  ...props
}: NumberFlowProps) {
  return (
    <NumberFlowPrimitive
      className={cn("tabular-nums", className)}
      isolate={isolate}
      respectMotionPreference={respectMotionPreference}
      {...props}
    />
  )
}

/** NumberFlow 的 format 参数类型，供下面几个封装的取值用 */
type NumberFlowFormat = NonNullable<NumberFlowProps["format"]>

/** CurrencyNumberFlow 的 props 定义（格式化由组件内部算好，不接受外部 format） */
type CurrencyNumberFlowProps = Omit<
  NumberFlowProps,
  "format" | "prefix" | "suffix" | "value"
> & {
  value: number
  /** 货币符号前缀，默认 "$" */
  prefix?: string
  /** 绝对值达到该阈值后不再显示小数（默认 100：≥100 取整，<100 显示两位） */
  fractionThreshold?: number
}

/**
 * CurrencyNumberFlow：货币格式的数字。小数位随数值大小自动伸缩：
 * 大额（≥ threshold）取整，小额保留两位。aria-label 保证屏幕阅读器
 * 能读到完整金额。
 */
function CurrencyNumberFlow({
  value,
  prefix = "$",
  fractionThreshold = 100,
  locales,
  "aria-label": ariaLabel,
  ...props
}: CurrencyNumberFlowProps) {
  const fractionDigits = Math.abs(value) >= fractionThreshold ? 0 : 2
  const format: NumberFlowFormat = {
    minimumFractionDigits: fractionDigits,
    maximumFractionDigits: fractionDigits,
    useGrouping: false,
  }
  const formatted = new Intl.NumberFormat(locales, format).format(value)

  return (
    <NumberFlow
      value={value}
      locales={locales}
      prefix={prefix}
      format={format}
      aria-label={ariaLabel ?? `${prefix}${formatted}`}
      {...props}
    />
  )
}

/** CompactNumberFlow 的 props 定义 */
type CompactNumberFlowProps = Omit<
  NumberFlowProps,
  "format" | "suffix" | "value"
> & {
  value: number
  /** 单位大小写：'upper' 用 K/M/B/T，'lower' 用 k/m/b/t */
  suffixCase?: "lower" | "upper"
}

/** 紧凑格式用的量级档位：除数 + 大小写两种后缀 */
const COMPACT_UNITS = [
  { divisor: 1, upperSuffix: undefined, lowerSuffix: undefined },
  { divisor: 1_000, upperSuffix: "K", lowerSuffix: "k" },
  { divisor: 1_000_000, upperSuffix: "M", lowerSuffix: "m" },
  { divisor: 1_000_000_000, upperSuffix: "B", lowerSuffix: "b" },
  { divisor: 1_000_000_000_000, upperSuffix: "T", lowerSuffix: "t" },
] as const

/**
 * CompactNumberFlow：紧凑缩写格式（1200 → 1.2K）。位数只保留 1 位小数；
 * 若进位后仍 ≥1000（如 999.6K），再升一档避免出现 1000K。
 */
function CompactNumberFlow({
  value,
  suffixCase = "upper",
  locales,
  "aria-label": ariaLabel,
  ...props
}: CompactNumberFlowProps) {
  const magnitude = Math.abs(value)
  // 找到第一个「值 ≥ 除数」的最大档位
  let unitIndex = COMPACT_UNITS.findLastIndex(
    ({ divisor }) => magnitude >= divisor,
  )
  unitIndex = Math.max(unitIndex, 0)

  let unit = COMPACT_UNITS[unitIndex]!
  let scaledValue = value / unit.divisor

  // 缩放后四舍五入到 1 位仍 ≥1000（例如 999.9K），说明该升到下一档
  if (
    Math.abs(Number(scaledValue.toFixed(1))) >= 1_000 &&
    unitIndex < COMPACT_UNITS.length - 1
  ) {
    unit = COMPACT_UNITS[unitIndex + 1]!
    scaledValue = value / unit.divisor
  }

  const roundedValue = Number(scaledValue.toFixed(1))
  const fractionDigits =
    unit.divisor > 1 && !Number.isInteger(roundedValue) ? 1 : 0
  const suffix =
    suffixCase === "upper" ? unit.upperSuffix : unit.lowerSuffix
  const format: NumberFlowFormat = {
    minimumFractionDigits: fractionDigits,
    maximumFractionDigits: fractionDigits,
    // 只有未缩写的纯数字才用千分位分组
    useGrouping: unit.divisor === 1,
  }
  const formatted = new Intl.NumberFormat(locales, format).format(scaledValue)

  return (
    <NumberFlow
      value={scaledValue}
      locales={locales}
      suffix={suffix}
      format={format}
      aria-label={ariaLabel ?? `${formatted}${suffix ?? ""}`}
      {...props}
    />
  )
}

/** CappedNumberFlow 的 props 定义 */
type CappedNumberFlowProps = Omit<
  NumberFlowProps,
  "format" | "suffix" | "value"
> & {
  value: number
  /** 封顶值：超过后显示 max+ 的形式，默认 99 */
  max?: number
}

/**
 * CappedNumberFlow：带封顶的数字徽标（如未读数）。数值夹在 [0, max]，
 * 超过 max 时显示 `max+`（如 99+）。
 */
function CappedNumberFlow({
  value,
  max = 99,
  "aria-label": ariaLabel,
  ...props
}: CappedNumberFlowProps) {
  const cappedValue = Math.min(Math.max(value, 0), max)

  return (
    <NumberFlow
      value={cappedValue}
      suffix={value > max ? "+" : undefined}
      format={{ maximumFractionDigits: 0, useGrouping: false }}
      aria-label={ariaLabel ?? String(value)}
      {...props}
    />
  )
}

export {
  CappedNumberFlow,
  CompactNumberFlow,
  CurrencyNumberFlow,
  NumberFlow,
  NumberFlowGroup,
}
export type {
  CappedNumberFlowProps,
  CompactNumberFlowProps,
  CurrencyNumberFlowProps,
  NumberFlowProps,
}
