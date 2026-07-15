import type { MemberRole } from "../types";

/**
 * Inputs to every permission rule. Stays role-typed so we don't have to thread
 * `MemberWithUser` (with PII) into pure logic — only what we actually need.
 *
 * `userId === null` models the logged-out edge case; `role === null` models the
 * "not a workspace member" / "member list still loading" case. Both must
 * gracefully deny without throwing.
 */
export interface PermissionContext {
  userId: string | null;
  role: MemberRole | null;
}

/**
 * Stable enum of *why* a permission was denied (or allowed). Lets UIs pick
 * different copy / disabled states / banner variants without parsing the
 * `message` string. Tests assert on `reason`.
 */
export type DecisionReason =
  | "allowed"
  | "not_authenticated"
  | "not_member"
  | "not_resource_owner"
  | "private_visibility"
  | "unknown";

export interface Decision<Reason extends DecisionReason = DecisionReason> {
  allowed: boolean;
  reason: Reason;
  /**
   * Human-readable copy for tooltips / banners. Centralised here so view code
   * doesn't drift. UI may still wrap it for emphasis but should not invent
   * its own copy.
   */
  message: string;
}

/** Builder helpers — keeps rules.ts tight. */
export const ALLOW: Decision<"allowed"> = {
  allowed: true,
  reason: "allowed",
  message: "",
};

export function deny<Reason extends Exclude<DecisionReason, "allowed">>(
  reason: Reason,
  message: string,
): Decision<Reason> {
  return { allowed: false, reason, message };
}
