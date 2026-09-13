// 可持久化的 Agent 草稿（StoredAgentDraft）与可编辑草稿（AgentDraft）之间的互转，
// 以及"两份草稿是否一样"的比较。
import type { StoredAgentDraft } from "../types";
import { EMPTY_AGENT_DRAFT, type AgentDraft } from "./draft";

export type { StoredAgentDraft };

/** 把内存里的可编辑草稿转成可序列化存储的形态（Set 展开为数组），供本地持久化。 */
export function toStoredAgentDraft(
  draft: AgentDraft,
  appliedMessageId: string | null,
): StoredAgentDraft {
  return {
    name: draft.name,
    description: draft.description,
    instructions: draft.instructions,
    conversation_starters: draft.conversationStarters,
    avatar_url: draft.avatarUrl,
    model: draft.model,
    thinking_level: draft.thinkingLevel,
    service_tier: draft.serviceTier,
    skill_ids: [...draft.skillIds],
    permission_scope: draft.permissionScope,
    member_ids: [...draft.memberIds],
    team_ids: [...draft.teamIds],
    applied_message_id: appliedMessageId,
  };
}

/**
 * 从存储形态重建出可编辑草稿。`runtimeId` 由调用方传入，因为 runtime 归服务端管——
 * 见 {@link StoredAgentDraft} 上的说明。
 */
export function fromStoredAgentDraft(
  stored: StoredAgentDraft,
  runtimeId: string,
): AgentDraft {
  return {
    ...EMPTY_AGENT_DRAFT,
    name: stored.name,
    description: stored.description,
    instructions: stored.instructions,
    conversationStarters: stored.conversation_starters ?? [],
    avatarUrl: stored.avatar_url,
    runtimeId,
    model: stored.model,
    thinkingLevel: stored.thinking_level,
    serviceTier: stored.service_tier,
    skillIds: new Set(stored.skill_ids),
    permissionScope: stored.permission_scope,
    memberIds: new Set(stored.member_ids),
    teamIds: new Set(stored.team_ids),
  };
}

/**
 * 判断两份草稿序列化后是否完全相同——即自动保存的"真有东西变了吗"检测。
 * 用存储形态比较，使"用相同成员重建的 Set"不会被误读为一次编辑、触发无意义的写入。
 */
export function storedAgentDraftsEqual(
  a: StoredAgentDraft,
  b: StoredAgentDraft,
): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}
