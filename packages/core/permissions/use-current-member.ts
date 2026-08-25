"use client";

import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "../auth";
import { memberListOptions } from "../workspace/queries";
import type { MemberWithUser } from "../types";
import type { PermissionContext } from "./types";

export function resolveCurrentMember(
  members: readonly MemberWithUser[] | undefined,
  userId: string | null | undefined,
): PermissionContext {
  const normalizedUserId = userId ?? null;
  const member = members?.find((candidate) => candidate.user_id === normalizedUserId);
  return {
    userId: normalizedUserId,
    role: member?.role ?? null,
  };
}

/**
 * Resolves the current user's membership in the given workspace. Single source
 * of truth for "what role am I" — replaces ad-hoc `members.find(...)` lookups
 * scattered across the views.
 *
 * `wsId` is explicit so this hook stays usable before a workspace route has
 * resolved.
 */
export function useCurrentMember(wsId: string): PermissionContext {
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const { data: members } = useQuery(memberListOptions(wsId));
  return resolveCurrentMember(members, userId);
}
