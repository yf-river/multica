import type {
  Agent,
  MemberRole,
  Skill,
} from "../types";
import { ALLOW, deny, type Decision, type PermissionContext } from "./types";

/**
 * Pure permission rules — single source of truth that mirrors the Go backend
 * gates in `server/internal/handler/`. Hooks in `use-resource-permissions.ts`
 * are thin wrappers that pull `PermissionContext` from auth + member queries
 * and forward to these.
 *
 * Returning a `Decision` (not a boolean) lets every surface — disabled state,
 * tooltip, banner copy — read the same `reason` and stay consistent without
 * sprinkling copy through the view layer.
 */

const isAdminLike = (role: MemberRole | null) =>
  role === "owner" || role === "admin";

const canManageOwnedResource = (
  ownerId: string | null,
  ctx: PermissionContext,
) => isAdminLike(ctx.role) || (ownerId !== null && ownerId === ctx.userId);

type EditDecisionReason =
  | "allowed"
  | "not_authenticated"
  | "not_resource_owner";
type AssignmentDecisionReason =
  | "allowed"
  | "not_authenticated"
  | "not_member"
  | "private_visibility";

// ---- Agents ----------------------------------------------------------------

/**
 * Update / archive / restore agent fields. The backend gates archive and
 * restore identically to edit (`server/internal/handler/agent.go:519-535`),
 * so callers can use `canEditAgent` for all three.
 */
export function canEditAgent(
  agent: Agent,
  ctx: PermissionContext,
): Decision<EditDecisionReason> {
  if (ctx.userId === null) {
    return deny("not_authenticated", "请先登录后再编辑智能体。");
  }
  if (canManageOwnedResource(agent.owner_id, ctx)) return ALLOW;
  return deny(
    "not_resource_owner",
    "只有智能体所有者和工作区管理员可以编辑这个智能体。",
  );
}

/**
 * Assign an agent to an issue. Workspace-scope agents are assignable by
 * any workspace member; personal agents are restricted to their owner plus
 * workspace admins/owners. Mirrors `issue.go:1471-1490`.
 */
export function canAssignAgentToIssue(
  agent: Agent,
  ctx: PermissionContext,
): Decision<AssignmentDecisionReason> {
  if (ctx.userId === null) {
    return deny("not_authenticated", "请先登录后再分配智能体。");
  }
  if (agent.scope === "workspace") {
    if (ctx.role === null) {
      return deny("not_member", "加入这个工作区后才能分配智能体。");
    }
    return ALLOW;
  }
  // scope === "personal"
  if (canManageOwnedResource(agent.owner_id, ctx)) return ALLOW;
  return deny(
    "private_visibility",
    "这是个人智能体，只有所有者和工作区管理员可以分配任务。",
  );
}

// ---- Skills ----------------------------------------------------------------

export function canEditSkill(
  skill: Pick<Skill, "created_by">,
  ctx: PermissionContext,
): Decision<EditDecisionReason> {
  if (ctx.userId === null) {
    return deny("not_authenticated", "请先登录后再编辑技能。");
  }
  if (canManageOwnedResource(skill.created_by, ctx)) return ALLOW;
  return deny(
    "not_resource_owner",
    "只有创建者和工作区管理员可以编辑这个技能。",
  );
}
