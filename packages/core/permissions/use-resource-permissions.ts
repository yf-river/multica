"use client";

import type { Agent, Skill } from "../types";
import { useCurrentMember } from "./use-current-member";
import { canEditAgent, canEditSkill } from "./rules";
import { deny, type Decision, type PermissionContext } from "./types";

type ResourceEditDecision = Decision<
  "allowed" | "not_authenticated" | "not_resource_owner" | "unknown"
>;

const PENDING: ResourceEditDecision = deny("unknown", "");

function useResourceEditPermission<Resource>(
  resource: Resource | null,
  wsId: string,
  rule: (resource: Resource, context: PermissionContext) => ResourceEditDecision,
): ResourceEditDecision {
  const context = useCurrentMember(wsId);
  return resource === null ? PENDING : rule(resource, context);
}

/**
 * Per-resource hook that returns a `Decision` for every relevant capability.
 * Each hook calls `useCurrentMember()` once and threads the context into the
 * pure rules in `rules.ts`.
 *
 * `wsId` is explicit so the hook stays usable outside a resolved route.
 *
 * Resource = `null` collapses every Decision to a denied "unknown" — keeps
 * callers branch-free during loading.
 *
 * `canArchive` / `canRestore` / `canManage` are deliberately not exposed:
 * the backend gates them identically to `canEdit`, so callers can use
 * `canEdit` everywhere and read better at the call site.
 */
export function useAgentEditPermission(
  agent: Agent | null,
  wsId: string,
): ResourceEditDecision {
  return useResourceEditPermission(agent, wsId, canEditAgent);
}

export function useSkillEditPermission(
  skill: Skill | null,
  wsId: string,
): ResourceEditDecision {
  return useResourceEditPermission(skill, wsId, canEditSkill);
}
