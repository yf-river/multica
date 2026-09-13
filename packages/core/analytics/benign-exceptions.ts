// 本文件识别并过滤「已知无害」的浏览器异常，避免它们污染 `$exception` 遥测数据。

// 这些是纯噪音的浏览器异常，在 `before_send` 里被整体丢弃（不仅仅是
// exception-dedupe.ts 里的去重），因为它们不携带任何可行动信号、浏览器会自行恢复，
// 而且规模一大就会淹没真实故障、白白烧掉计费的事件额度。
//
// ResizeObserver "loop ..." 错误是最典型的例子：规范规定，当观测回调在单个动画帧内
// 没跑完时浏览器就会抛这个错；下一帧浏览器会继续投递，所以实际上什么都没坏。
// 每个直接或间接（通过 UI 库）使用 ResizeObserver 的应用都会产生它。CSSWG 明确认为
// 它是无害的——见 w3c/csswg-drafts#5023。不同 Chrome 版本的报错文案略有差异，但都
// 包含 "ResizeObserver loop" 这个片段。
//
// 往这里加规则的准入门槛很高：必须是无害、可自恢复、且没有任何可行动信号的错误。
// 真实的 bug 绝不能被打进冷宫——拿不准时，交给去重保险丝（只限制重复次数）处理。

const BENIGN_MESSAGE_PATTERNS: RegExp[] = [/ResizeObserver loop/i];

/**
 * 判断这个 `$exception` 事件是否为「已知无害」的浏览器噪音、应当被整体丢弃。
 * 从（脱敏前的）事件属性里读取消息——匹配的消息不含 PII，直接读原文是安全的，
 * 而且在脱敏之前匹配可以避免擦洗过程破坏信号。永不抛异常：任何意外结构都返回
 * `false`（保留事件），这是 `before_send` 要求的「故障时放行」方向。
 *
 * @param properties - `$exception` 事件的属性对象（可能为空）
 * @returns 若为无害噪音返回 `true`（丢弃），否则返回 `false`（保留）
 */
export function isBenignException(
  properties: Record<string, unknown> | undefined,
): boolean {
  if (!properties || typeof properties !== "object") return false;

  const messages: unknown[] = [properties.$exception_message];
  const list = properties.$exception_list;
  if (Array.isArray(list)) {
    for (const entry of list) {
      if (entry && typeof entry === "object" && "value" in entry) {
        messages.push((entry as { value: unknown }).value);
      }
    }
  }

  for (const message of messages) {
    if (typeof message !== "string") continue;
    for (const pattern of BENIGN_MESSAGE_PATTERNS) {
      if (pattern.test(message)) return true;
    }
  }
  return false;
}
