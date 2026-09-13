import { useRef } from "react";

/**
 * workspace 模块：记录在本布局实例生命周期内至少成功解析过一次的工作区 slug。
 * 用于区分两种情况：
 *
 *  - 「刚被移除的当前工作区」（slug 之前见过，现在没了）——调用方通常正在
 *    跳走（删除/退出操作的 mutation，或实时 workspace:deleted 事件）。在跳转
 *    完成前的这个窗口期渲染 NoAccessPage 会闪一下刺眼的「工作区不可用」。
 *    这里返回 `true`，让布局在导航解析期间渲染 null。
 *
 *  - 「URL 指向我从未有权访问的工作区」（slug 从未见过）——真正的无权访问
 *    场景。返回 `false`，让布局渲染带恢复按钮的 NoAccessPage。
 *
 * 作用域：每个布局实例一个 Set。工作区布局一旦卸载（如桌面端标签页关闭），
 * 这份记忆随之丢弃——这也正确，因为那个视图用户反正已经失去了。
 */
export function useWorkspaceSeen(
  slug: string | undefined,
  resolved: boolean,
): boolean {
  const seenRef = useRef<Set<string>>(new Set());
  if (resolved && slug) seenRef.current.add(slug);
  return slug ? seenRef.current.has(slug) : false;
}
