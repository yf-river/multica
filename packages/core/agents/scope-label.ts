import type { AgentScope } from "../types";

/**
 * Display labels for agent scope.
 */
export const SCOPE_LABEL: Record<AgentScope, string> = {
  workspace: "Workspace",
  personal: "Personal",
};

export function scopeLabel(v: AgentScope): string {
  return SCOPE_LABEL[v];
}
