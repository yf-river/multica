// ---------------------------------------------------------------------------
// 选择器按键策略——回答"什么键移动高亮"和"什么键确认"，由产品里所有带高亮的
// 列表共享。
// ---------------------------------------------------------------------------
//
// 放在这里而不是挨着编辑器的建议弹层，是因为使用者已不只有编辑器选择器：
// Issue 详情的线程导航器也用了同一套导航策略，而为了一个纯键盘判断去 import
// Tiptap 扩展模块，会把整个编辑器依赖图拖进头部弹层。

/**
 * 判断哪些键"确认"当前高亮的建议行。
 *
 * `Enter` 是标准确认键（WAI-ARIA combobox 指南）。普通 `Tab` 是额外便利键，
 * 契合终端 / CLI / 编辑器补全的肌肉记忆（MUL-3685）。`Shift+Tab` 以及任何
 * `Ctrl/Cmd/Alt + Tab` 刻意不作为确认键：它们保留反向焦点导航 / 系统窗口切换
 * 语义，以维持标准键盘可访问性。
 *
 * 把规则集中在这里，能让所有基于 `createSuggestionPopupRender` 构建的选择器
 * （mention、slash-skill、内置命令、以及未来任何建议列表）保持一致，而不是
 * 每个列表各自重新决定什么算"确认"。调用方用它替代裸的 `event.key === "Enter"`
 * 判断，于是 `Tab` 在它们的确认分支里成为 `Enter` 的严格别名。
 * @param event 键盘事件。
 * @returns 是否为确认键。
 */
export function isPickerAcceptKey(event: KeyboardEvent): boolean {
  if (event.key === "Enter") return true;
  return (
    event.key === "Tab" &&
    !event.shiftKey &&
    !event.ctrlKey &&
    !event.metaKey &&
    !event.altKey
  );
}

/** 按键在建议列表中移动高亮的方向。 */
export type PickerNavigationDirection = "next" | "prev";

/**
 * 把一次按键解析为建议列表的移动方向，不是导航键时返回 `null`。
 *
 * 方向键是标准绑定。`Ctrl+N`/`Ctrl+J`（向下）和 `Ctrl+P`/`Ctrl+K`（向上）是
 * 额外的 Emacs/readline 别名，命令栏本就免费支持：它基于 cmdk，其 `vimBindings`
 * 选项（默认开启）恰好映射这一组。这里对齐后，选择器就不再是产品里唯一一处
 * 该肌肉记忆失效的地方（MUL-5495）。
 *
 * 基于 `Cmd` 的别名被刻意排除。cmdk 也不绑定它们，所以命令栏从不支持，且
 * `Cmd+P`/`Cmd+N` 是浏览器和系统的快捷键（打印、新窗口），网页无法占用——
 * 与 `@multica/core/shortcuts` 里 `isReservedShortcut` 的规则一致。
 *
 * 字母别名要求单独按 `Ctrl`。搭配其他修饰键时该组合属于浏览器或系统
 * （`Ctrl+Shift+N` 打开无痕窗口），所以要原样放行。方向键保持与修饰键无关，
 * 和以前完全一样。
 *
 * 集中到 `isPickerAcceptKey` 旁边，能让所有基于 `createSuggestionPopupRender`
 * 构建的选择器以相同方式导航，而不是每个列表各自重新决定什么算"向下移动"。
 * @param event 键盘事件。
 * @returns 移动方向，或 null（非导航键）。
 */
export function pickerNavigationDirection(
  event: KeyboardEvent,
): PickerNavigationDirection | null {
  if (event.key === "ArrowDown") return "next";
  if (event.key === "ArrowUp") return "prev";
  if (!event.ctrlKey || event.metaKey || event.altKey || event.shiftKey) {
    return null;
  }
  // 转小写，因为 Caps Lock 开启时上报的是 "N" 而非设置 shiftKey。
  switch (event.key.toLowerCase()) {
    case "n":
    case "j":
      return "next";
    case "p":
    case "k":
      return "prev";
    default:
      return null;
  }
}
