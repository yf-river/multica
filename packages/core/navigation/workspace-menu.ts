export const WORKSPACE_MENU_KEYS = [
  "issues",
  "projects",
  "autopilots",
  "agents",
  "squads",
  "runReviews",
  "debug",
  "evaluation",
  "runtimes",
  "skills",
  "settings",
] as const;

export type WorkspaceMenuKey = (typeof WORKSPACE_MENU_KEYS)[number];

type WorkspaceSettings = Record<string, unknown> | null | undefined;

/** Missing entries are intentionally visible for backwards compatibility. */
export function isWorkspaceMenuVisible(settings: WorkspaceSettings, key: WorkspaceMenuKey): boolean {
  const visibility = settings?.sidebar_menu_visibility;
  if (!visibility || typeof visibility !== "object" || Array.isArray(visibility)) return true;
  const value = (visibility as Record<string, unknown>)[key];
  return value !== false;
}

export function withWorkspaceMenuVisibility(
  settings: WorkspaceSettings,
  key: WorkspaceMenuKey,
  visible: boolean,
): Record<string, unknown> {
  const current = settings && typeof settings === "object" ? settings : {};
  const oldVisibility = current.sidebar_menu_visibility;
  const visibility = oldVisibility && typeof oldVisibility === "object" && !Array.isArray(oldVisibility)
    ? { ...(oldVisibility as Record<string, unknown>) }
    : {};
  visibility[key] = visible;
  return { ...current, sidebar_menu_visibility: visibility };
}
