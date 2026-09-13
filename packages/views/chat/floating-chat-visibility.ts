// 悬浮聊天浮层的路由闸门：判断当前路由是否应该隐藏悬浮聊天。
/**
 * 悬浮聊天浮层的路由闸门，被浮层本身和 `toggleChat` 键盘快捷键共用，
 * 确保两者永远不会出现分歧。
 *
 * 在聊天标签页上，整页界面已经接管了同一个 `activeSessionId`，所以悬浮副本纯属重复——
 * `FloatingChat` 在那里什么都不渲染。快捷键也必须遵守同样的规则：
 * 在一个浮层无法挂载的路由上翻转 `isOpen`，看起来就像按了一个死键，
 * 而且会在下一次导航时突然弹出一个窗口（或消失），让用户措手不及。
 *
 * @param pathname - 当前路由路径。
 * @param chatPath - 聊天页的路径（用于前缀判断）。
 * @returns 如果该路由应隐藏悬浮聊天则返回 true。
 */
export function isFloatingChatRouteSuppressed(
  pathname: string,
  chatPath: string,
): boolean {
  return pathname === chatPath || pathname.startsWith(`${chatPath}/`);
}
