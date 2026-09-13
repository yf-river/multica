// 本文件职责：判断「当前用户能否编辑/删除某个 skill」的权限逻辑——一个 React hook
// 版本（自己拉取成员角色）与一个纯函数版本（角色现成时直接算），给 UI 用来隐藏/禁用
// 操作按钮。
"use client";

import { useQuery } from "@tanstack/react-query";
import type { MemberRole, SkillSummary } from "@multica/core/types";
import { useAuthStore } from "@multica/core/auth";
import { memberListOptions } from "@multica/core/workspace/queries";

/**
 * 当前用户能否编辑/删除给定 skill。
 *
 * 规则：工作区 admin 与 owner 可编辑任意 skill；其余人只能编辑自己创建的 skill。
 * 服务端会独立执行这套校验；本 hook 只是镜像它，让 UI 能直接隐藏/禁用操作，
 * 而不是等一个 403 回来。
 *
 * `wsId` 显式传入（而不是从 WorkspaceIdProvider 读），是为了让本 hook 能在
 * 工作区上下文尚未接线的组件里也能用，也为了让调用方一眼看清权限检查的作用域。
 * 与仓库里「工作区相关 hook 都显式收 wsId」的约定一致。
 */
export function useCanEditSkill(
  skill: SkillSummary | null | undefined,
  wsId: string,
): boolean {
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const { data: members = [] } = useQuery(memberListOptions(wsId));

  if (!skill) return false;
  const myRole = members.find((m) => m.user_id === userId)?.role ?? null;
  return canEditSkill(skill, { userId, role: myRole });
}

/**
 * 无 hook 版本——供手上已有角色与 userId 的地方直接调用
 * （例如列表行在整页只算一次角色后复用）。
 */
export function canEditSkill(
  skill: SkillSummary,
  opts: { userId: string | null; role: MemberRole | null },
): boolean {
  if (opts.role === "admin" || opts.role === "owner") return true;
  return skill.created_by === opts.userId;
}
