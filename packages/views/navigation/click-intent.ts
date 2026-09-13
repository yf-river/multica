/**
 * navigation 模块：把鼠标事件映射为「导航意图」的唯一位置，依据导航行为规范：
 *
 *   普通点击          -> "push"             （原地跳转）
 *   cmd/ctrl + 点击   -> "background-tab"   （新标签打开，焦点留在本页）
 *   cmd/ctrl + shift  -> "foreground-tab"   （新标签打开，焦点移过去）
 *   中键点击          -> "background-tab"
 *
 * 单独的 Shift 刻意不算修饰键：浏览器对它的处理不一致（Chrome/Firefox 开新
 * 窗口，Safari 加入阅读列表），所以映射成 "push"。在 Web 上，渲染真实
 * <a> 的界面应把带修饰键的点击交给浏览器，而不是查询本函数——原生处理才是
 * 得到真正后台标签的唯一途径。
 */
export type LinkClickIntent = "push" | "background-tab" | "foreground-tab";

/**
 * 根据事件上的按键与修饰键解析点击意图。
 * @param e 事件中与点击相关的字段。
 */
export function resolveClickIntent(
  e: Pick<MouseEvent, "button" | "metaKey" | "ctrlKey" | "shiftKey">,
): LinkClickIntent {
  if (e.button === 1) return "background-tab";
  if (e.metaKey || e.ctrlKey) {
    return e.shiftKey ? "foreground-tab" : "background-tab";
  }
  return "push";
}
