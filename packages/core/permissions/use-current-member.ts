// 本文件提供「当前用户在某个工作区里的成员身份」的 Hook。

"use client";

import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "../auth";
import type { MemberRole, MemberWithUser } from "../types";
import { memberListOptions } from "../workspace/queries";

/**
 * 解析当前用户在给定工作区里的成员身份。「我是什么角色」的唯一真源——
 * 取代散落在各视图里的临时 `members.find(...)` 查找。
 *
 * `wsId` 显式传入（不通过 `useWorkspaceId()` Context），让本 hook 在组件树
 * 尚未接好工作区上下文时也可用，符合仓库对工作区感知 hook 的规则。
 *
 * @param wsId - 工作区 id
 * @returns 用户 id、角色（都不是成员则为 null）、成员记录与加载态
 */
export function useCurrentMember(wsId: string): {
  userId: string | null;
  role: MemberRole | null;
  member: MemberWithUser | null;
  isLoading: boolean;
} {
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const { data: members, isLoading } = useQuery(memberListOptions(wsId));
  const member = members?.find((m) => m.user_id === userId) ?? null;
  return {
    userId,
    role: member?.role ?? null,
    member,
    isLoading,
  };
}
