// 客户端敏感信息脱敏兜底：把智能体输出里可能夹带的密钥、口令等替换成占位符。
// 服务端已做主要脱敏，这里是展示层的最后一道安全网——防止任何漏网的凭据直接
// 渲染到界面上被人看到。
//
// 逐条正则替换，匹配到的敏感片段换成统一格式的 "[REDACTED ...]" 标记。

const patterns: { re: RegExp; replacement: string }[] = [
  // AWS 访问密钥 ID（以 AKIA 开头的一串大写字母数字）
  { re: /\bAKIA[0-9A-Z]{16}\b/g, replacement: "[REDACTED AWS KEY]" },
  // AWS 私密访问密钥（"aws_secret_access_key" 之类键名后跟 40 位 base64）
  { re: /(?:aws_secret_access_key|secret_?access_?key)\s*[=:]\s*[A-Za-z0-9/+=]{40}/gi, replacement: "[REDACTED AWS SECRET]" },
  // PEM 私钥块（-----BEGIN ... PRIVATE KEY----- 到 -----END ...----- 整段）
  { re: /-----BEGIN[A-Z\s]*PRIVATE KEY-----[\s\S]*?-----END[A-Z\s]*PRIVATE KEY-----/g, replacement: "[REDACTED PRIVATE KEY]" },
  // GitHub OAuth / 经典令牌（ghp_/gho_/ghu_/ghs_/ghr_ 前缀）
  { re: /\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{36,255}\b/g, replacement: "[REDACTED GITHUB TOKEN]" },
  // GitHub fine-grained PAT（github_pat_ 前缀，2022 年起推荐的新格式）
  { re: /\bgithub_pat_[A-Za-z0-9_]{20,255}\b/g, replacement: "[REDACTED GITHUB TOKEN]" },
  // Google API 密钥（AIza 开头，如 Gemini / Maps / Firebase 用的）
  { re: /\bAIza[0-9A-Za-z_-]{35}([^0-9A-Za-z_-]|$)/g, replacement: "[REDACTED GOOGLE API KEY]$1" },
  // GitLab 个人访问令牌
  { re: /\bglpat-[A-Za-z0-9_-]{20,}\b/g, replacement: "[REDACTED GITLAB TOKEN]" },
  // OpenAI / Anthropic API 密钥（sk- 前缀）
  { re: /\bsk-[A-Za-z0-9_-]{20,}\b/g, replacement: "[REDACTED API KEY]" },
  // Slack 令牌（xoxb/xoxp/xoxo/xoxs/xoxr 前缀）
  { re: /\bxox[bporas]-[A-Za-z0-9-]{10,}\b/g, replacement: "[REDACTED SLACK TOKEN]" },
  // JWT（三段式 base64，用点分隔）
  { re: /\bey[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b/g, replacement: "[REDACTED JWT]" },
  // Bearer 认证头（保留 "Bearer" 字样，只打码后面的令牌）
  { re: /\bBearer\s+[A-Za-z0-9\-._~+/]+=*/gi, replacement: "Bearer [REDACTED]" },
  // 内嵌密码的连接串（scheme://user:password@ 形式，打码到 @ 为止）
  { re: /(?:postgres|mysql|mongodb|redis|amqp)(?:ql)?:\/\/[^:\s]+:[^@\s]+@/gi, replacement: "[REDACTED CONNECTION STRING]@" },
  // 通用密钥环境变量赋值（API_KEY=xxx、PASSWORD:xxx 等）
  { re: /(?:API_KEY|API_SECRET|SECRET_KEY|SECRET|ACCESS_TOKEN|AUTH_TOKEN|PRIVATE_KEY|DATABASE_URL|DB_PASSWORD|DB_URL|REDIS_URL|PASSWORD|TOKEN)\s*[=:]\s*\S+/gi, replacement: "[REDACTED CREDENTIAL]" },
];

/**
 * 对文本执行全部脱敏规则，把匹配到的凭据替换为占位符。
 *
 * 说明：这是服务端脱敏之外展示层的兜底网——不能也不该被视为唯一的防线，
 * 只能保证"漏网的敏感串不至于直接渲染上屏"。
 * @param text 原始文本（智能体输出、命令输出等）。
 * @returns 脱敏后的文本；无敏感内容时与原样一致。
 */
export function redactSecrets(text: string): string {
  let result = text;
  for (const { re, replacement } of patterns) {
    result = result.replace(re, replacement);
  }
  return result;
}
