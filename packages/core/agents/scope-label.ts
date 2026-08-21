import type { AgentScope } from "../types";

/**
 * Display labels for agent scope.
 */
export const SCOPE_LABEL: Record<AgentScope, string> = {
  workspace: "Workspace",
  personal: "Personal",
};

/**
 * Honest descriptions for assignability.
 */
export const SCOPE_DESCRIPTION: Record<AgentScope, string> = {
  workspace: "All members can assign",
  personal: "Only you and workspace admins can assign",
};

/** Tooltip suitable for read-only badges on hover/list rows. */
export const SCOPE_TOOLTIP: Record<AgentScope, string> = {
  workspace: "Workspace — all members can assign",
  personal: "Personal — only you and workspace admins can assign",
};

export function scopeLabel(v: AgentScope): string {
  return SCOPE_LABEL[v];
}
