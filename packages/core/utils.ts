// 本文件提供一组通用前端工具函数：UUID/ID 生成、IME 组合输入判断、文本截断。

/**
 * 生成符合 RFC 4122 规范的 UUID v4 字符串。
 *
 * 实现原理：用 `crypto.getRandomValues` 取 16 字节随机数，再按规范把第 6、8 字节
 * 打上版本号和变体标记，最后格式化成常见的 `8-4-4-4-12` 形式。
 *
 * @returns 形如 `xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx` 的 UUID 字符串
 */
export function generateUUID(): string {
  const cryptoObj = globalThis.crypto;

  if (!cryptoObj?.getRandomValues) {
    throw new Error("Secure UUID generation requires crypto.getRandomValues");
  }

  const bytes = new Uint8Array(16);
  cryptoObj.getRandomValues(bytes);

  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40; // 版本号 4
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80; // 变体 1

  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");

  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/**
 * 生成一个安全 ID：优先使用浏览器原生的 `crypto.randomUUID`，
 * 在非安全上下文（无 randomUUID 或调用失败）时回退到 `generateUUID`。
 *
 * @returns 一个 UUID 字符串
 */
export function createSafeId(): string {
  const cryptoObj = globalThis.crypto;

  if (cryptoObj?.randomUUID) {
    try {
      return cryptoObj.randomUUID();
    } catch {
      // 原生命中失败，走下面的回退逻辑。
    }
  }

  return generateUUID();
}

/** 生成请求 ID，用于日志/链路追踪的 header。默认去掉连字符后截取前 8 位。 */
export function createRequestId(length = 8): string {
  return createSafeId().replace(/-/g, "").slice(0, length);
}

/**
 * 判断键盘事件是否发生在 IME 组合输入过程中（例如中文拼音、日文假名）。
 * 用于提交组合文本的那次 Enter，绝不能触发表单提交/发送/创建等处理逻辑。
 *
 * 同时兼容 React 合成事件和原生 DOM `KeyboardEvent`。
 *
 * 为什么要同时检查 `isComposing` 和 `keyCode === 229`：
 * - `isComposing` 是标准信号，但 Safari 在结束组合的那次 keydown 时已把它清掉，
 *   只检查它就会漏掉真正提交的那次 Enter。
 * - 组合输入期间浏览器会把 keyCode 上报为 229（与真实按键无关），
 *   这条判断在 Safari 的边缘场景下依然有效。
 *
 * 有 `nativeEvent` 时始终读它——React 合成事件会被规范化，而原生事件才反映浏览器真实状态。
 */
export function isImeComposing(event: {
  isComposing?: boolean;
  keyCode?: number;
  nativeEvent?: { isComposing?: boolean; keyCode?: number };
}): boolean {
  const e = event.nativeEvent ?? event;
  return Boolean(e.isComposing) || e.keyCode === 229;
}

/**
 * 把 `text` 截断到最多 `maxLength` 个码点，超长时在末尾追加省略号（`…`，U+2026）。
 * 未超长时原样返回。
 *
 * 省略号本身计入长度限制，因此截断后的结果最多 `maxLength` 个码点：
 * 可见文本被截到 `maxLength - 1` 个字符，去掉尾部空白后再拼上省略号。
 * 长度按 Unicode 码点（而非 UTF-16 码元）计算，所以 emoji 不会被切成半个代理对——
 * 返回的字符串其 `.length`（码元数）可能超过 `maxLength`，但可见字符数始终不超。
 * 当 `maxLength` 连一个可见字符都放不下时只返回省略号；`maxLength <= 0` 时返回空串。
 */
export function truncateWithEllipsis(text: string, maxLength: number): string {
  // 先用码元数做快速判断：遇到 astral 字符时可能不会提前返回，但绝不会误判截断——
  // 下面的码点检查才是最终依据。
  if (text.length <= maxLength) {
    return text;
  }
  if (maxLength <= 1) {
    return maxLength <= 0 ? "" : "…";
  }
  const chars = Array.from(text);
  if (chars.length <= maxLength) {
    return text;
  }
  return `${chars.slice(0, maxLength - 1).join("").trimEnd()}…`;
}
