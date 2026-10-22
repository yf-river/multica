import type { WorkspacePageKey } from "../paths/route-icons";

/** Workspace-wide navigation preference; hiding an entry never changes access rights. */
export const CONFIGURABLE_MENU_KEYS = [
  "inbox", "issues", "projects", "autopilots", "agents", "squads", "usage",
  "runReviews", "runtimes", "skills", "mcp", "quickActions", "upstream", "health", "meals", "goals", "life",
] as const satisfies readonly WorkspacePageKey[];

export function isWorkspaceMenuVisible(settings: Record<string, unknown> | undefined, key: string): boolean {
  if (key === "myIssues") return false;
  if (key === "chat" || key === "settings") return true;
  const hidden = settings?.hidden_menu_items;
  return !Array.isArray(hidden) || !hidden.includes(key);
}
