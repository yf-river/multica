"use client";

import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "../auth";
import { memberListOptions } from "../workspace/queries";
import type { PermissionContext } from "./types";

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
  const member = members?.find((m) => m.user_id === userId) ?? null;
  return {
    userId,
    role: member?.role ?? null,
  };
}
