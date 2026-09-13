// 时间格式化工具：把经过的秒/毫秒格式化成 `Ns` 或 `Nm Ms` 的可读字符串。
/**
 * 把经过的秒数格式化成 `Ns`（一分钟以内）或 `Nm Ms`（超过一分钟）。
 * 当余数为 0 时省略秒部分，让整分钟的读数更短（"3m" 而不是 "3m 0s"）。
 * 被实时 StatusPill 计时器和持久的 assistant-message 计时行共用——
 * 让它们保持同步，避免中途的 "Working · 38s" 和最终 "Replied in 39s"
 * 标题之间出现可见的偏差。
 *
 * @param secs - 经过的秒数。
 * @returns 格式化后的耗时字符串。
 */
export function formatElapsedSecs(secs: number): string {
  if (secs < 60) return `${secs}s`;
  const m = Math.floor(secs / 60);
  const s = secs % 60;
  return s ? `${m}m ${s}s` : `${m}m`;
}

/** 便捷方法：同样的格式化，但输入是毫秒（服务端存储的 elapsed_ms）。 */
export function formatElapsedMs(ms: number): string {
  return formatElapsedSecs(Math.max(0, Math.round(ms / 1000)));
}
