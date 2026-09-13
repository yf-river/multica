// 本文件定义权限模块的公共类型：每条权限规则的输入上下文，以及「允许/拒绝
// + 原因」的判定结果。

import type { MemberRole } from "../types";

/**
 * 每条权限规则的输入。保持「角色类型」形态，这样不必把 `MemberWithUser`
 *（带 PII）塞进纯逻辑——只传真正需要的字段。
 *
 * `userId === null` 表示「未登录」的边界情况；`role === null` 表示「不是
 * 工作区成员」/「成员列表仍在加载」。两者都必须优雅地拒绝而不是抛错。
 */
export interface PermissionContext {
  userId: string | null;
  role: MemberRole | null;
}

/**
 * 对「为什么拒绝（或放行）」的稳定枚举。让 UI 可以按 `reason` 选择不同的
 * 文案/禁用态/横幅变体，而无需解析 `message` 字符串。测试也断言在 `reason`
 * 上。
 */
export type DecisionReason =
  | "allowed"
  | "not_authenticated"
  | "not_member"
  | "not_owner_role"
  | "not_admin_role"
  | "not_resource_owner"
  | "last_owner"
  | "private_visibility"
  | "unknown";

/** 一条权限判定结果。 */
export interface Decision {
  allowed: boolean;
  reason: DecisionReason;
  /**
   * 供 tooltip / 横幅使用的人类可读文案。集中在这里以免视图代码各自漂移；
   * UI 可以给它加强调样式，但不应自创文案。
   */
  message: string;
}

/** 构造辅助——让 rules.ts 保持精简：放行。 */
export const ALLOW: Decision = {
  allowed: true,
  reason: "allowed",
  message: "",
};

/** 构造辅助——让 rules.ts 保持精简：拒绝。 */
export function deny(reason: DecisionReason, message: string): Decision {
  return { allowed: false, reason, message };
}
