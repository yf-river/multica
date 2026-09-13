// 本文件实现「纯权限规则」——镜像 Go 后端 `server/internal/handler/` 里闸门的
// 唯一真源。`use-resource-permissions.ts` 里的 Hook 是薄封装：从 auth + 成员
// 查询里取出 `PermissionContext` 后转发到这里。
//
// 返回 `Decision`（而不是布尔）让每个界面——禁用态、tooltip、横幅文案——读到
// 同一个 `reason` 并保持一致，不必在视图层到处撒文案。

import type {
  Agent,
  Comment,
  Member,
  MemberRole,
  RuntimeDevice,
  Skill,
} from "../types";
import { ALLOW, deny, type Decision, type PermissionContext } from "./types";

/** 角色是否为「类管理员」（owner 或 admin）。 */
const isAdminLike = (role: MemberRole | null) =>
  role === "owner" || role === "admin";

// ---- Agents ----------------------------------------------------------------

/**
 * 编辑 / 归档 / 恢复 agent 字段。后端把归档和恢复与编辑同等设闸
 * （`server/internal/handler/agent.go:519-535`），所以调用方三种操作都可以用
 * `canEditAgent`。
 */
export function canEditAgent(agent: Agent, ctx: PermissionContext): Decision {
  if (ctx.userId === null) {
    return deny("not_authenticated", "Sign in to edit this agent.");
  }
  if (isAdminLike(ctx.role)) return ALLOW;
  if (agent.owner_id !== null && agent.owner_id === ctx.userId) return ALLOW;
  return deny(
    "not_resource_owner",
    "Only the agent owner and workspace admins can edit this agent.",
  );
}

/**
 * 能否调用某个 agent——把它派给 issue、@它、与它聊天或触发运行。
 *
 * 镜像 MUL-3963 的后端调用闸门，它读的是 `permission_mode` + `invocation_targets`
 *（不是派生的 `visibility` 字段）：
 *
 *   - 拥有者：永远可以
 *   - permission_mode "private"：只有拥有者。这是关键行为变化——工作区管理员
 *     不再能绕过私有 agent。
 *   - permission_mode "public_to" + workspace 目标：任意工作区成员
 *   - permission_mode "public_to" + member 目标：只有被点名的用户
 *   - team 目标：保留，v1 里惰性（永不授予）
 */
export function canAssignAgentToIssue(
  agent: Agent,
  ctx: PermissionContext,
): Decision {
  if (ctx.userId === null) {
    return deny("not_authenticated", "Sign in to assign agents.");
  }

  // 拥有者无论什么模式都可以调用自己的 agent。
  if (agent.owner_id !== null && agent.owner_id === ctx.userId) {
    return ALLOW;
  }

  if (agent.permission_mode === "private") {
    return deny(
      "private_visibility",
      "Personal agent — only the owner can assign work.",
    );
  }

  // permission_mode === "public_to"：解析调用授权。workspace 授权把调用权开放
  // 给任意工作区成员。`?? []` 是为了防老自托管后端 / 过期缓存——类型说必填
  // 数组，但它们可能省略该字段（GH #4915）。
  const targets = agent.invocation_targets ?? [];
  if (targets.some((t) => t.target_type === "workspace")) {
    if (ctx.role === null) {
      return deny("not_member", "Join this workspace to assign agents.");
    }
    return ALLOW;
  }

  // member 授权把调用权开放给恰好被点名的用户。team 目标保留，在 v1 里惰性
  // ——永不授予。
  if (
    targets.some(
      (t) => t.target_type === "member" && t.target_id === ctx.userId,
    )
  ) {
    return ALLOW;
  }

  return deny(
    "private_visibility",
    "Restricted agent — you don't have access to assign work to it.",
  );
}

// ---- Skills ----------------------------------------------------------------

/** 能否编辑技能：管理员或创建者。 */
export function canEditSkill(skill: Skill, ctx: PermissionContext): Decision {
  if (ctx.userId === null) {
    return deny("not_authenticated", "Sign in to edit this skill.");
  }
  if (isAdminLike(ctx.role)) return ALLOW;
  if (skill.created_by !== null && skill.created_by === ctx.userId) {
    return ALLOW;
  }
  return deny(
    "not_resource_owner",
    "Only the creator and workspace admins can edit this skill.",
  );
}

/** 能否删除技能：与 canEditSkill 同一套规则。 */
export function canDeleteSkill(skill: Skill, ctx: PermissionContext): Decision {
  return canEditSkill(skill, ctx);
}

// ---- Comments --------------------------------------------------------------

/** 能否编辑评论：成员作者本人或管理员；agent 写的评论任何人不可改。 */
export function canEditComment(
  comment: Comment,
  ctx: PermissionContext,
): Decision {
  if (ctx.userId === null) {
    return deny("not_authenticated", "Sign in to edit comments.");
  }
  // 只有成员写的评论可编辑；agent 写的评论对任何人类都不可变。
  if (comment.author_type !== "member") {
    return deny(
      "not_resource_owner",
      "Agent-authored comments cannot be edited.",
    );
  }
  if (comment.author_id === ctx.userId) return ALLOW;
  if (isAdminLike(ctx.role)) return ALLOW;
  return deny(
    "not_resource_owner",
    "Only the author and workspace admins can edit this comment.",
  );
}

/** 能否删除评论：作者本人或管理员。 */
export function canDeleteComment(
  comment: Comment,
  ctx: PermissionContext,
): Decision {
  if (ctx.userId === null) {
    return deny("not_authenticated", "Sign in to delete comments.");
  }
  if (comment.author_type === "member" && comment.author_id === ctx.userId) {
    return ALLOW;
  }
  if (isAdminLike(ctx.role)) return ALLOW;
  return deny(
    "not_resource_owner",
    "Only the author and workspace admins can delete this comment.",
  );
}

// ---- Runtimes --------------------------------------------------------------

/** 能否删除 runtime：拥有者或管理员。 */
export function canDeleteRuntime(
  runtime: RuntimeDevice,
  ctx: PermissionContext,
): Decision {
  if (ctx.userId === null) {
    return deny("not_authenticated", "Sign in to delete runtimes.");
  }
  if (isAdminLike(ctx.role)) return ALLOW;
  if (runtime.owner_id !== null && runtime.owner_id === ctx.userId) {
    return ALLOW;
  }
  return deny(
    "not_resource_owner",
    "Only the runtime owner and workspace admins can delete this runtime.",
  );
}

// ---- Workspace -------------------------------------------------------------

/** 能否更新工作区设置：管理员。 */
export function canUpdateWorkspaceSettings(ctx: PermissionContext): Decision {
  if (isAdminLike(ctx.role)) return ALLOW;
  return deny(
    "not_admin_role",
    "Only workspace owners and admins can update workspace settings.",
  );
}

/** 能否删除工作区：只有 owner。 */
export function canDeleteWorkspace(ctx: PermissionContext): Decision {
  if (ctx.role === "owner") return ALLOW;
  return deny(
    "not_owner_role",
    "Only the workspace owner can delete this workspace.",
  );
}

/** 能否管理工作区成员：管理员。 */
export function canManageMembers(ctx: PermissionContext): Decision {
  if (isAdminLike(ctx.role)) return ALLOW;
  return deny(
    "not_admin_role",
    "Only workspace owners and admins can manage members.",
  );
}

/**
 * 编码自 `workspace.go:458-530` 的角色变更矩阵：
 *   - 管理员不能碰 owner 角色（既不能把 owner 降级，也不能往 owner 提升）
 *   - 最后一个 owner 不能被降级
 *   - 非管理员完全不能改角色
 *
 * `ownerCount` 是当前工作区里 role=owner 的成员数。调用方用缓存的成员列表在
 * 本地推导。
 */
export function canChangeMemberRole(
  target: Pick<Member, "role">,
  ownerCount: number,
  ctx: PermissionContext,
): Decision {
  const manage = canManageMembers(ctx);
  if (!manage.allowed) return manage;

  if (target.role === "owner") {
    if (ctx.role !== "owner") {
      return deny(
        "not_owner_role",
        "Only the workspace owner can change another owner's role.",
      );
    }
    if (ownerCount <= 1) {
      return deny(
        "last_owner",
        "Promote another member to owner first — a workspace must keep at least one owner.",
      );
    }
  }
  return ALLOW;
}
