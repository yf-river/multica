// 本文件是剪贴板写入工具：优先用异步 Clipboard API，非安全上下文（纯 http://）
// 下自动回退到隐藏 textarea + execCommand('copy') 的旧方案。

/**
 * 把文本复制到剪贴板，并为非安全上下文（纯 http://）提供回退方案。
 *
 * 异步 Clipboard API（`navigator.clipboard`）只在安全上下文暴露——`https://`
 * 或 `localhost`。在纯 `http://` 源上是 `undefined`，于是
 * `navigator.clipboard.writeText` 会抛错、复制静默失败（这正是自托管于 http
 * 的 bug 报告背后的症状）。当安全 API 不可用时，回退到隐藏 `<textarea>` +
 * 旧版 `document.execCommand('copy')`，它在非安全上下文也能工作。
 *
 * @param text 要复制的文本
 * @returns 成功返回 `true`，失败返回 `false`。调用方应依据返回值决定成功
 * 副作用（toast、「已复制」勾选态），返回 false 时向用户提示错误。
 */
export async function copyText(text: string): Promise<boolean> {
  // 首选路径：异步 Clipboard API（仅安全上下文）。
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // 权限被拒 / 文档未聚焦 / 被拦截——落入下面的旧路径而不是直接判失败。
    }
  }

  // 回退路径：隐藏 textarea + execCommand('copy')。纯 http:// 下可用。
  if (typeof document === "undefined") return false;

  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  // 视觉隐藏且不参与布局/滚动
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "0";
  textarea.style.width = "1px";
  textarea.style.height = "1px";
  textarea.style.padding = "0";
  textarea.style.border = "none";
  textarea.style.opacity = "0";
  textarea.style.pointerEvents = "none";

  // 记住当前焦点元素，临时选区操作结束后恢复焦点——不打扰可能正开着
  // 且持有复制按钮的菜单/弹出层。
  const previouslyFocused =
    document.activeElement instanceof HTMLElement ? document.activeElement : null;

  document.body.appendChild(textarea);
  try {
    textarea.focus();
    textarea.select();
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    document.body.removeChild(textarea);
    previouslyFocused?.focus();
  }
}
