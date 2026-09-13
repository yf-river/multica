// 汇总 chat 未读数：所有会话的未读助手消息总数（IM 风格）。
import type { ChatSession } from "../types/chat";

/**
 * Chat 未读徽标数字：跨会话的未读助手*消息*总数（IM 风格），不是"有未读的会话数"。
 *
 * 所有渲染聚合 chat 未读数界面的单一真相来源（web/桌面侧边栏、移动端标签栏）——
 * 同一账号状态各平台必须显示同一个 N（见 apps/mobile/CLAUDE.md 的 "Behavioral
 * parity"）。纯函数，移动端可直接 import。
 *
 * `excludeSessionId` 会把一个会话从求和里剔除：用户当前正在读的那个会话。
 * 线程列表把那个会话自己的徽标显示为 0（它的未读即将被查看者的自动已读清除），
 * 若聚合计数还带着它，就会显示一个列表解释不了的数字。调用方只在聊天界面确实
 * 展示那个会话时传它；后台指针（例如聊天页关闭时记住的选择）绝不能剔除，
 * 否则新回复永远不会冒徽标。
 * @param sessions 会话列表（可能尚未加载）。
 * @param excludeSessionId 从总和里剔除的会话 id（当前正在阅读的那个），可选。
 * @returns 未读助手消息总数。
 */
export function countUnreadChatMessages(
  sessions: readonly ChatSession[] | undefined,
  excludeSessionId?: string | null,
): number {
  if (!sessions) return 0;
  return sessions.reduce(
    (sum, s) =>
      s.id === excludeSessionId ? sum : sum + (s.unread_count ?? 0),
    0,
  );
}
