import type { ResourceScope } from "../types";

/**
 * Display labels for agent scope.
 */
export const SCOPE_LABEL: Record<ResourceScope, string> = {
  workspace: "Workspace",
  personal: "Personal",
};

export function scopeLabel(v: ResourceScope): string {
  return SCOPE_LABEL[v];
}
