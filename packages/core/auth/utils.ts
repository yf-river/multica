// 本文件提供登录后重定向 URL 的安全校验工具。

/**
 * 校验登录后的重定向 URL，仅在安全可跳转时才返回它。
 *
 * 只接受单斜杠的相对路径（如 `/invite/abc`）。不安全或空输入返回 `null`——回退路径由
 * 调用点决定，这样这个工具就不会把「用户没传 next」和某个特定路径耦合在一起。
 *
 * 拒绝以下情况：
 *   - `null` / 空字符串
 *   - 绝对 URL（`https://evil.com`、`javascript:alert(1)` 等）
 *   - 协议相对 URL（`//evil.com`）
 *   - 含反斜杠的路径（Windows 风格或 `/\\host`）
 *   - 含 ASCII 控制字符的路径（`\x00`–`\x1f`）
 *
 * @param raw - 待校验的重定向 URL
 * @returns 安全的相对路径，否则 null
 */
export function sanitizeNextUrl(raw: string | null): string | null {
  if (!raw) return null;
  if (!raw.startsWith("/") || raw.startsWith("//")) return null;
  // eslint-disable-next-line no-control-regex -- 有意为之：拒绝控制字符正是目的所在
  if (/[\x00-\x1f\\]/.test(raw)) return null;
  return raw;
}
