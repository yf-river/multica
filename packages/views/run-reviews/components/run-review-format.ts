// 运行复盘（run review）里用的各种格式化小工具：时间解析、时长/数字/百分比/
// 日期时间/JSON 格式化、文本截断、状态中文标签等，供复盘页各组件共用。

/** 把时间字符串/数字解析成毫秒时间戳；空值或无法解析返回 null。 */
export function parseTimeMs(value: string | null | undefined) {
  if (!value) return null;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}

/** 把毫秒时长格式化成「Xm Ys」（不足一分钟只显示秒）。 */
export function formatDuration(ms: number) {
  if (!ms || ms <= 0) return "0m";
  const totalSeconds = Math.round(ms / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes <= 0) return `${seconds}s`;
  return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`;
}

/** 数字千位分隔格式化（无小数）。 */
export function formatNumber(value: number) {
  return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 0 }).format(value || 0);
}

/** 把 0~1 的比率格式化成百分比；null 显示「暂无」。 */
export function formatPercent(value: number | null) {
  if (value === null) return "暂无";
  return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1, style: "percent" }).format(value);
}

/** 计算缓存命中（读）占缓存读写总量的比率；读写总量为 0 时返回 null。 */
export function cacheReuseRate(cacheReadTokens: number, cacheWriteTokens: number) {
  const denominator = cacheReadTokens + cacheWriteTokens;
  if (denominator <= 0) return null;
  return cacheReadTokens / denominator;
}

/** 把时间值格式化成「MM-DD HH:mm:ss」；空值显示「暂无」。 */
export function formatDateTime(value: string | number | null | undefined) {
  if (value === null || value === undefined || value === "") return "暂无";
  const ms = typeof value === "number" ? value : parseTimeMs(value);
  if (ms === null) return "暂无";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(new Date(ms));
}

/** 把任意值转成带缩进的 JSON 文本；序列化失败时退回 String()。 */
export function formatJSON(value: unknown) {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

/** 折叠空白并（必要时）截断到 max 长度，末尾加省略号。 */
export function truncateText(value: string, max: number) {
  const normalized = value.replace(/\s+/g, " ").trim();
  return normalized.length > max ? `${normalized.slice(0, max - 3)}...` : normalized;
}

/** 返回第一个「非空白字符串」参数（trim 后），全空则返回空串。 */
export function firstNonEmpty(...values: Array<string | undefined | null>) {
  return values.find((value) => typeof value === "string" && value.trim() !== "")?.trim() ?? "";
}

/** 把 unknown 安全转成 string（非字符串一律空串）。 */
export function stringFromUnknown(value: unknown) {
  return typeof value === "string" ? value : "";
}

/** 截短 id 到前 8 个字符（超出部分丢弃）。 */
export function shortId(value: string) {
  return value.length > 8 ? value.slice(0, 8) : value;
}

/** FNV-1a 32 位哈希：给文本算一个稳定的非负整数，常用于派生颜色/索引。 */
export function fnv1a32(value: string) {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

/** 把状态 key（任务/问题状态）映射成中文展示文案；未知状态原样返回。 */
export function statusLabel(status: string) {
  const labels: Record<string, string> = {
    backlog: "待规划",
    todo: "待办",
    in_progress: "进行中",
    in_review: "验收中",
    done: "已完成",
    completed: "已完成",
    failed: "失败",
    blocked: "阻塞",
    cancelled: "已取消",
    queued: "排队",
    running: "运行中",
  };
  return labels[status] ?? status;
}
