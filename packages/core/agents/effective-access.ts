// Agent 列表里展示的"有效访问范围"三态推导，以及批量访问弹窗的就绪判断。
import type { AgentInvocationTarget, AgentPermissionMode } from "../types";

/**
 * Agent 列表展示的三种有效访问范围，从权威的 `permission_mode` +
 * `invocation_targets` 推导（MUL-3963）。
 * 遗留的 `visibility` 字段是这两种的有损两态投影——一个指定了具体人的
 * `public_to` Agent 会映射成 `visibility: "private"`，和真正仅所有者可见无法区分——
 * 所以列表改用这个三态值展示。
 *
 * 映射镜像服务器的 `canInvokeAgent` 闸门
 * （`server/internal/handler/agent_access.go`）：
 *   - owner-only      = `private`（只有所有者能调用）
 *   - workspace       = `public_to` 且目标是 workspace（任意成员/Agent/系统）
 *   - specific-people = `public_to` 但没有 workspace 目标（member/team 目标）
 */
export type AccessScope = "workspace" | "specific-people" | "owner-only";

/**
 * 从 Agent 的权限字段推导有效访问范围。`permission_mode` 缺失时安全回退到
 * "owner-only"（旧版自托管后端或过期缓存可能省略这些字段）；`public_to` 但没有
 * `invocation_targets` 时维持 "specific-people"。
 * @param permissionMode Agent 的权限模式。
 * @param invocationTargets Agent 允许的调用目标。
 * @returns 三态的有效访问范围。
 */
export function effectiveAccessScope(
  permissionMode: AgentPermissionMode | undefined | null,
  invocationTargets: readonly AgentInvocationTarget[] | undefined | null,
): AccessScope {
  if (permissionMode !== "public_to") {
    return "owner-only";
  }
  if ((invocationTargets ?? []).some((t) => t.target_type === "workspace")) {
    return "workspace";
  }
  return "specific-people";
}

/** 全部可能的有效访问范围值，按展示顺序排列。 */
export const ALL_ACCESS_SCOPES: readonly AccessScope[] = [
  "workspace",
  "specific-people",
  "owner-only",
];

/**
 * 根据选择器当前 onChange 的载荷，判断批量访问弹窗的"应用"按钮是否可点。
 * `null`（还没选）以及 `public_to` 但没有调用目标都不算就绪。
 * @param change 批量选择器当前选中的访问设置（未选时为 null）。
 * @returns 是否已可应用。
 */
export function isAccessChangeReady(
  change: { permission_mode: AgentPermissionMode; invocation_targets: readonly { target_type: string }[] } | null,
): boolean {
  if (!change) return false;
  if (change.permission_mode === "private") return true;
  return change.invocation_targets.length > 0;
}
