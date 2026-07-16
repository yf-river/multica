/**
 * Public API for the permissions module.
 *
 * Exports only what the views currently consume. Adding a new rule to the
 * public API should follow the same minimum-surface pattern — only add it when
 * there is a current caller.
 */
export { canAssignAgentToIssue, canEditAgent, canEditSkill, canManageWorkspace } from "./rules";

export {
  useAgentEditPermission,
  useSkillEditPermission,
} from "./use-resource-permissions";

export { resolveCurrentMember, useCurrentMember } from "./use-current-member";
