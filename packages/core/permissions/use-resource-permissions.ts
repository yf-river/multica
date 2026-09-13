// 本文件提供「按资源」的权限 Hook：把当前成员身份接进纯规则集，返回该资源
// 各项能力的 Decision。

"use client";

import type { Agent, Skill } from "../types";
import { useCurrentMember } from "./use-current-member";
import {
  canAssignAgentToIssue,
  canDeleteSkill,
  canEditAgent,
  canEditSkill,
} from "./rules";
import { deny, type Decision } from "./types";

/** 资源尚未加载（或不在成员上下文里）时返回的「待定」判定。 */
const PENDING: Decision = deny("unknown", "");

/**
 * 按资源返回该资源各相关能力的 `Decision`。每个 hook 都调一次
 * `useCurrentMember()`，把上下文穿进 `rules.ts` 的纯规则。
 *
 * `wsId` 显式传入（不从 `WorkspaceIdProvider` 读），让 hook 在工作区上下文
 * 之外也可用——符合仓库对工作区感知 hook 的规则。
 *
 * 资源 = `null` 时把所有 Decision 塌缩成「拒绝的 unknown」——让调用方在加载期
 * 无需分支。
 *
 * 刻意不暴露 `canArchive` / `canRestore` / `canManage`：后端对它们与 `canEdit`
 * 同等设闸，调用方处处用 `canEdit` 即可，调用点读起来也更清楚。
 */
export function useAgentPermissions(
  agent: Agent | null,
  wsId: string,
): {
  canEdit: Decision;
  canAssign: Decision;
  isLoading: boolean;
} {
  const { userId, role, isLoading } = useCurrentMember(wsId);
  const ctx = { userId, role };
  // 成员查询在途时 `role` 是 null，下面的规则会把合法成员误判成拒绝
  // （例如对 public_to+workspace 的 agent 报 `not_member`）。有「永远可点」
  // 操作入口的调用方必须把 `isLoading` 当「未定」而非「拒绝」。
  if (agent === null) {
    return { canEdit: PENDING, canAssign: PENDING, isLoading };
  }
  return {
    canEdit: canEditAgent(agent, ctx),
    canAssign: canAssignAgentToIssue(agent, ctx),
    isLoading,
  };
}

/** 技能的权限 Hook：能否编辑 / 删除。 */
export function useSkillPermissions(
  skill: Skill | null,
  wsId: string,
): {
  canEdit: Decision;
  canDelete: Decision;
} {
  const { userId, role } = useCurrentMember(wsId);
  const ctx = { userId, role };
  if (skill === null) {
    return { canEdit: PENDING, canDelete: PENDING };
  }
  return {
    canEdit: canEditSkill(skill, ctx),
    canDelete: canDeleteSkill(skill, ctx),
  };
}
