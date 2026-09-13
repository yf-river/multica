/**
 * 按"计划所处的时钟"而非"读者本地时钟"来渲染时刻：一个写着 "18:00
 * (America/Los_Angeles)" 的自动运行计划，绝不能给 UTC+8 的读者打印成 09:00。
 * 措辞——月份名、12 还是 24 小时制——则按读者的 locale。
 *
 * 有两类可能出错的情况，但都不该把页面搞挂：
 * - 本浏览器 ICU 数据不含的时区（在新版本里保存的自动运行计划，被旧版桌面端
 *   打开）→ 回退到本地时间：会差一个偏移，但仍是"一天中的时刻"，而原始 ISO
 *   字符串不是；
 * - 根本不是日期的时间戳（后端数据漂移）→ 原样返回字符串本身。
 *   非法 Date 会让 Intl 抛异常，所以这里在格式化前检查而不是格式化后再
 *   catch：catch 只会再次执行同一个抛异常的调用。
 * @param iso ISO 时间字符串。
 * @param timeZone 目标时区（IANA 名称），可为 undefined。
 * @param locale 用于措辞的 locale。
 * @returns 格式化后的时间字符串。
 */
export function formatInTimeZone(
  iso: string,
  timeZone: string | undefined,
  locale: string,
): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return iso;
  const options: Intl.DateTimeFormatOptions = {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  };
  try {
    return new Intl.DateTimeFormat(locale, { ...options, timeZone }).format(at);
  } catch {
    return new Intl.DateTimeFormat(locale, options).format(at);
  }
}
