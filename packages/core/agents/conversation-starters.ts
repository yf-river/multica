// 对话开场白（conversation starter）的解析与能力判断：决定新对话展示什么开场白、
// 当前查看者能否从空状态入口编辑它。
import type { Agent, AgentConversationStarter } from "../types";

/**
 * 解析"与这个 Agent 开始新对话时实际展示什么开场白"。
 *
 * 一条已配置的建议只有"标题和正文都有内容"才算数：编辑器允许作者加一行，
 * 半填状态会拦下保存，但旧客户端发来的畸形载荷绝不能渲染出一个空白按钮。
 *
 * 没有一条完整建议存活时，用调用方的本地化兜底文案，且 `isFallback` 会如实说明——
 * 设置预览需要告诉作者"这些是默认项"，而不是暗示这些是他们配的。
 * @param configured Agent 已保存的开场白配置。
 * @param fallback 无有效配置时使用的默认开场白。
 * @returns 实际展示的开场白，以及是否在用兜底。
 */
export function selectConversationStarters(
  configured: AgentConversationStarter[] | undefined | null,
  fallback: AgentConversationStarter[],
): { starters: AgentConversationStarter[]; isFallback: boolean } {
  const complete = (configured ?? []).filter(
    (item) => item.label.trim() !== "" && item.prompt.trim() !== "",
  );
  return complete.length > 0
    ? { starters: complete, isFallback: false }
    : { starters: fallback, isFallback: true };
}

/**
 * 这个查看者是否应该在聊天空状态里被提供一个"编辑开场白"的入口。
 *
 * 三种保持沉默的独立理由，任一种都会产生一条撒谎的链接：
 *
 *  - 没有 Agent，或它已归档：它的对话只读，编辑开场白不会改变任何人看到的东西，
 *  - 后端不持久化开场白：Instructions 页在该环境不渲染编辑器，链接会落到无法兑现
 *    它的地方，
 *  - 查看者没有编辑该 Agent 的权限：读者会到达一个他没有写权限的页面。
 * @param agent 目标 Agent（可能为空）。
 * @param opts 后端是否支持开场白 + 当前查看者能否编辑该 Agent。
 * @returns 是否应展示"自定义开场白"入口。
 */
export function canCustomizeConversationStarters(
  agent: Pick<Agent, "archived_at"> | null,
  opts: { conversationStartersSupported: boolean; canEditAgent: boolean },
): boolean {
  if (!agent || agent.archived_at) return false;
  if (!opts.conversationStartersSupported) return false;
  return opts.canEditAgent;
}
