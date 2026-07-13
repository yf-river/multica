/**
 * Public API for the permissions module.
 *
 * Exports only what the views currently consume. Adding a new rule to the
 * public API should follow the same minimum-surface pattern — only add it when
 * there is a current caller.
 */
export type {
  Decision,
  DecisionReason,
  PermissionContext,
} from "./types";

export { canAssignAgentToIssue, canEditAgent } from "./rules";

export {
  useAgentPermissions,
  useSkillPermissions,
} from "./use-resource-permissions";
