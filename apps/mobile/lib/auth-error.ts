/**
 * Map backend account/password auth errors to user-facing Chinese strings.
 */
export function mapAuthError(err: unknown, fallback: string): string {
  if (!(err instanceof Error)) return fallback;
  const msg = err.message.toLowerCase();
  if (/invalid|incorrect|wrong/.test(msg)) {
    return "账号或密码不正确，请检查后重试。";
  }
  if (/rate.?limit|too many|throttle/.test(msg)) {
    return "尝试次数过多，请稍后再试。";
  }
  if (/network|fetch|timeout|unreachable/.test(msg)) {
    return "无法连接 Multica，请检查网络后重试。";
  }
  return fallback;
}
