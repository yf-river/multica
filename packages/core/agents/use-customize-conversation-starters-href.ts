"use client";

// 聊天空状态里的"自定义开场白"入口链接 hook：能编辑才返回指向该 Agent 配置页的链接。
import type { Agent } from "../types";
import { useConfigStore } from "../config";
import { useWorkspacePaths } from "../paths";
import { useAgentPermissions } from "../permissions";
import { canCustomizeConversationStarters } from "./conversation-starters";

/**
 * 聊天空状态里"自定义"入口应指向的地址；该查看者不应看到入口时返回 `null`。
 *
 * 两个聊天界面各自渲染空状态、来自不同的调用点，所以接线放这里，
 * 而规则本身在 `canCustomizeConversationStarters` 里。
 * @param agent 目标 Agent（可能为空）。
 * @param wsId 工作区 id。
 * @returns 可跳转的 href；无权限/不支持/Agent 不存在时返回 null。
 */
export function useCustomizeConversationStartersHref(
  agent: Agent | null,
  wsId: string,
): string | null {
  const conversationStartersSupported = useConfigStore(
    (state) => state.agentConversationStartersSupported,
  );
  const paths = useWorkspacePaths();
  const { canEdit } = useAgentPermissions(agent, wsId);

  if (
    !agent ||
    !canCustomizeConversationStarters(agent, {
      conversationStartersSupported,
      canEditAgent: canEdit.allowed,
    })
  ) {
    return null;
  }
  return paths.agentConversationStarters(agent.id);
}
