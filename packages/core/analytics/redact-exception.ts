// 本文件负责在 `$exception` 事件离开客户端前，擦洗其中的 PII（个人可识别信息）。

// 异常自动采集（`capture_exceptions: true`）会发送错误消息和堆栈。栈帧是代码位置
// （文件 / 行号 / 函数），是安全的；但消息经常插值了用户输入——带输入值的校验错误、
// 带原文的解析错误、可能带 token 的 URL 的网络错误。我们保留诊断形状（类型 + 帧 +
// 消息中非敏感的部分），并脱敏那些携带用户数据的模式。作为 posthog-js 的
// `before_send` 接线；见 initAnalytics。

const REDACTED = "[redacted]";

// 顺序很重要：先剥离查询字符串，再做通用的长 token 规则，这样 URL 的 host 本身
// 才不会被误伤粉碎。
const PATTERNS: Array<[RegExp, string]> = [
  // 邮箱。
  [/[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}/gi, REDACTED],
  // URL 的 query/fragment（可能携带 token / PII）——保留 scheme+host+path。
  [/((?:https?|file|multica):\/\/[^\s?#]*)[?#]\S*/gi, `$1?${REDACTED}`],
  // 长的不透明 token：JWT、API key、UUID、会话 id（24 个字符以上）。
  [/\b[A-Za-z0-9_-]{24,}\b/g, REDACTED],
];

/** 把自由文本中的 PII 类子串脱敏。非字符串或空串原样返回。 */
export function redactText(input: unknown): unknown {
  if (typeof input !== "string" || input.length === 0) return input;
  let out = input;
  for (const [pattern, replacement] of PATTERNS) {
    out = out.replace(pattern, replacement);
  }
  return out;
}

/**
 * 原地脱敏 `$exception` 事件属性中面向用户的字符串：顶层消息，以及 `$exception_list`
 * 里每个条目的 `value`。类型和栈帧保持不动（它们是代码位置，不是用户数据）。
 * 返回同一个属性对象以便链式调用。
 *
 * @param properties - 异常事件属性（可为空）
 * @returns 脱敏后的同一属性对象
 */
export function redactExceptionProperties(
  properties: Record<string, unknown> | undefined,
): Record<string, unknown> | undefined {
  if (!properties || typeof properties !== "object") return properties;

  if ("$exception_message" in properties) {
    properties.$exception_message = redactText(properties.$exception_message);
  }

  const list = properties.$exception_list;
  if (Array.isArray(list)) {
    for (const entry of list) {
      if (entry && typeof entry === "object" && "value" in entry) {
        (entry as { value: unknown }).value = redactText(
          (entry as { value: unknown }).value,
        );
      }
    }
  }

  return properties;
}
