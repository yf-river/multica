export function parseTimeMs(value: string | null | undefined) {
  if (!value) return null;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}

export function formatDuration(ms: number) {
  if (!ms || ms <= 0) return "0m";
  const totalSeconds = Math.round(ms / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes <= 0) return `${seconds}s`;
  return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`;
}

export function formatNumber(value: number) {
  return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 0 }).format(value || 0);
}

export function formatPercent(value: number | null) {
  if (value === null) return "暂无";
  return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1, style: "percent" }).format(value);
}

export function cacheReuseRate(cacheReadTokens: number, cacheWriteTokens: number) {
  const denominator = cacheReadTokens + cacheWriteTokens;
  if (denominator <= 0) return null;
  return cacheReadTokens / denominator;
}

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

export function formatJSON(value: unknown) {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export function truncateText(value: string, max: number) {
  const normalized = value.replace(/\s+/g, " ").trim();
  return normalized.length > max ? `${normalized.slice(0, max - 3)}...` : normalized;
}

export function firstNonEmpty(...values: Array<string | undefined | null>) {
  return values.find((value) => typeof value === "string" && value.trim() !== "")?.trim() ?? "";
}

export function stringFromUnknown(value: unknown) {
  return typeof value === "string" ? value : "";
}

export function shortId(value: string) {
  return value.length > 8 ? value.slice(0, 8) : value;
}

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
