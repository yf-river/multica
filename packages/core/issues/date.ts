// 议题日期工具：把“日历日”的 "YYYY-MM-DD" 字符串与 Date 安全互转，避免时区导致日期错位。
//
// 议题的 start_date / due_date 是日历日，不是时刻：选择器没有时分秒输入，
// 所以“3 月 1 日”对任何时区的观众都必须是 3 月 1 日。它们以纯日期的 "YYYY-MM-DD"
// 字符串传输。这些辅助函数在该字符串和 Date 之间转换时，绝不因本地时区而把日子偏移——
// GH #3618 / MUL-2925 背后的 bug 就是把本地午夜的 Date 用 toISOString()（会注入时区）
// 序列化、又按 UTC 日期边界读回来。
//
// 只含纯函数（无 React / DOM），以便与移动端共享。

const DATE_ONLY = /^(\d{4})-(\d{2})-(\d{2})/;

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

/**
 * 把用户在日历里挑的 Date（所选日期的本地午夜）序列化成 "YYYY-MM-DD" 字符串，
 * 用的是**本地**日历分量，保证存下来的日子就是用户点的那一天。
 */
export function toDateOnly(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/** 返回观看者本地日历里的今天，格式 "YYYY-MM-DD"。 */
export function todayDateOnly(): string {
  return toDateOnly(new Date());
}

/** 返回观看者本地日历里、距今天 `days` 天的那一天的 "YYYY-MM-DD"。 */
export function addDaysDateOnly(days: number): string {
  const d = new Date();
  d.setDate(d.getDate() + days);
  return toDateOnly(d);
}

/**
 * 把纯日期字符串解析成 [年, 月, 日]；对遗留的完整 ISO 时间戳，读它的 UTC 日历日
 * 来兼容。无法解析时返回 null。
 */
function parseParts(value: string): [number, number, number] | null {
  const m = DATE_ONLY.exec(value);
  if (m) return [Number(m[1]), Number(m[2]), Number(m[3])];
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return null;
  return [d.getUTCFullYear(), d.getUTCMonth() + 1, d.getUTCDate()];
}

/**
 * 锚定在“该日历日的 UTC 午夜”的 Date。用于时区安全的展示（用 `timeZone: "UTC"` 格式化）、
 * Gantt 的按天分桶，以及按时间先后比较。
 */
export function dateOnlyToUTCDate(
  value: string | null | undefined,
): Date | null {
  if (!value) return null;
  const parts = parseParts(value);
  if (!parts) return null;
  return new Date(Date.UTC(parts[0], parts[1] - 1, parts[2]));
}

/**
 * 锚定在“该日历日的本地午夜”的 Date。用于日历选择器的 `selected` / `defaultMonth`，
 * 它们按本地时间的天来匹配。
 */
export function dateOnlyToLocalDate(
  value: string | null | undefined,
): Date | undefined {
  if (!value) return undefined;
  const parts = parseParts(value);
  if (!parts) return undefined;
  return new Date(parts[0], parts[1] - 1, parts[2]);
}

/**
 * 把一个日历日格式化成展示文本，且时区安全（日子不会随观看者时区偏移）。
 * 值为空或无法解析时返回 ""。
 */
export function formatDateOnly(
  value: string | null | undefined,
  options: Intl.DateTimeFormatOptions = { month: "short", day: "numeric" },
  locale?: string,
): string {
  const d = dateOnlyToUTCDate(value);
  if (!d) return "";
  return d.toLocaleDateString(locale, { ...options, timeZone: "UTC" });
}

/** 当该日历日严格早于今天（观看者本地日）时返回 true。 */
export function isPastDateOnly(value: string | null | undefined): boolean {
  const d = dateOnlyToUTCDate(value);
  if (!d) return false;
  const today = dateOnlyToUTCDate(todayDateOnly());
  return today != null && d.getTime() < today.getTime();
}
