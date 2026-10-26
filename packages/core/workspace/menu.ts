import {
  LIFE_TABS,
  parseLifeTab,
  type LifeTab,
  type WorkspacePaths,
} from "../paths/paths";
import type { NavLabelKey } from "../paths/route-icons";

/** Sidebar sections that own a configurable set of navigation rows. */
export type WorkspaceMenuGroupId =
  | "personal"
  | "workspace"
  | "configure"
  | "living"
  | "life";

/**
 * Where a group title comes from in the layout locale: sidebar sections keep
 * `workspace_group` / `configure_group`, everyday sections use `nav.living` /
 * `nav.life`. Unlabeled groups (the inbox row) omit this entirely.
 */
export type WorkspaceMenuGroupLabel =
  | { section: "sidebar"; key: "workspace_group" | "configure_group" }
  | { section: "nav"; key: "living" | "life" };

/** One sidebar row and the preference key that hides it. */
export interface WorkspaceMenuEntry {
  /** Persisted in `workspace.settings.hidden_menu_items`. */
  key: string;
  /** `layout.nav.<navKey>` label. */
  navKey: NavLabelKey;
  /** Workspace-scoped destination of this row. */
  href: (paths: WorkspacePaths) => string;
  /** Entries that can never be hidden (chat, settings). */
  fixed?: boolean;
  /** Life rows are active only for their own `?tab=` value. */
  lifeTab?: LifeTab;
  /** Extra route roots that also keep this row active (meals ↔ cooking). */
  alsoActiveOn?: (paths: WorkspacePaths) => string[];
}

/** A sidebar section: title plus the rows rendered under it. */
export interface WorkspaceMenuGroup {
  id: WorkspaceMenuGroupId;
  /** Omitted groups are unlabeled, exactly like the inbox row today. */
  label?: WorkspaceMenuGroupLabel;
  entries: readonly WorkspaceMenuEntry[];
}

/**
 * The single source of truth for workspace navigation. The sidebar renders
 * these rows and the "workspace menu" settings render one switch per row, so
 * the two surfaces cannot drift again (a row without a switch or vice versa).
 */
export const WORKSPACE_MENU_GROUPS: readonly WorkspaceMenuGroup[] = [
  {
    id: "personal",
    entries: [
      { key: "inbox", navKey: "inbox", href: (paths) => paths.inbox() },
    ],
  },
  {
    id: "workspace",
    label: { section: "sidebar", key: "workspace_group" },
    entries: [
      { key: "chat", navKey: "chat", href: (paths) => paths.chat(), fixed: true },
      { key: "issues", navKey: "issues", href: (paths) => paths.issues() },
      { key: "projects", navKey: "projects", href: (paths) => paths.projects() },
      { key: "autopilots", navKey: "autopilots", href: (paths) => paths.autopilots() },
      { key: "agents", navKey: "agents", href: (paths) => paths.agents() },
      { key: "squads", navKey: "squads", href: (paths) => paths.squads() },
      { key: "usage", navKey: "usage", href: (paths) => paths.usage() },
      { key: "runReviews", navKey: "run_reviews", href: (paths) => paths.runReviews() },
      { key: "knowledge", navKey: "knowledge", href: (paths) => paths.knowledge() },
    ],
  },
  {
    id: "configure",
    label: { section: "sidebar", key: "configure_group" },
    entries: [
      { key: "runtimes", navKey: "runtimes", href: (paths) => paths.runtimes() },
      { key: "skills", navKey: "skills", href: (paths) => paths.skills() },
      { key: "mcp", navKey: "mcp", href: (paths) => paths.mcp() },
      { key: "quickActions", navKey: "quick_actions", href: (paths) => paths.quickActions() },
      { key: "upstream", navKey: "upstream", href: (paths) => paths.upstream() },
      { key: "settings", navKey: "settings", href: (paths) => paths.settings(), fixed: true },
    ],
  },
  {
    id: "living",
    label: { section: "nav", key: "living" },
    entries: [
      { key: "health", navKey: "health", href: (paths) => paths.health() },
      {
        key: "meals",
        navKey: "meals",
        href: (paths) => paths.meals(),
        alsoActiveOn: (paths) => [paths.cooking()],
      },
      { key: "goals", navKey: "goals", href: (paths) => paths.goals() },
    ],
  },
  {
    id: "life",
    label: { section: "nav", key: "life" },
    entries: LIFE_TABS.map((tab) => ({
      key: tab,
      navKey: tab,
      href: (paths) => paths.lifeTab(tab),
      lifeTab: tab,
    })),
  },
];

const WORKSPACE_MENU_ENTRIES = WORKSPACE_MENU_GROUPS.flatMap((group) => group.entries);

/** Every row that can be hidden; fixed rows (chat, settings) are excluded. */
export const CONFIGURABLE_MENU_KEYS: readonly string[] = WORKSPACE_MENU_ENTRIES
  .filter((entry) => entry.fixed !== true)
  .map((entry) => entry.key);

/** Destination of a sidebar row for the current workspace. */
export function workspaceMenuItemHref(
  entry: WorkspaceMenuEntry,
  paths: WorkspacePaths,
): string {
  return entry.href(paths);
}

/** Current location used to decide which sidebar row is active. */
export interface WorkspaceMenuLocation {
  pathname: string;
  searchParams: URLSearchParams;
}

/**
 * Whether a sidebar row is the current destination. Life rows compare their
 * `?tab=` value (missing tab means the page's default `memory` tab); every
 * other row keeps the established prefix match so detail routes stay active.
 */
export function isWorkspaceMenuItemActive(
  entry: WorkspaceMenuEntry,
  { pathname, searchParams, paths }: WorkspaceMenuLocation & { paths: WorkspacePaths },
): boolean {
  if (entry.lifeTab) {
    return (
      pathname === paths.life() &&
      parseLifeTab(searchParams.get("tab")) === entry.lifeTab
    );
  }
  const matches = (href: string) => pathname === href || pathname.startsWith(`${href}/`);
  if (matches(workspaceMenuItemHref(entry, paths))) return true;
  return (entry.alsoActiveOn?.(paths) ?? []).some(matches);
}

/** Workspace-wide navigation preference; hiding an entry never changes access rights. */
export function isWorkspaceMenuVisible(
  settings: Record<string, unknown> | undefined,
  key: string,
): boolean {
  if (key === "myIssues") return false;
  if (key === "chat" || key === "settings") return true;
  const hidden = settings?.hidden_menu_items;
  if (!Array.isArray(hidden)) return true;
  if (hidden.includes(key)) return false;
  // Legacy workspaces hid the whole life group through one aggregate "life" key.
  return !(hidden.includes("life") && LIFE_TABS.includes(key as LifeTab));
}

/**
 * The complete hidden list after toggling one row. Rebuilding it from the model
 * also normalizes a legacy aggregate `"life"` into the four tab keys and drops
 * stale keys that no longer exist, so the next save converges.
 */
export function nextHiddenMenuKeys(
  settings: Record<string, unknown> | undefined,
  toggledKey: string,
  visible: boolean,
): string[] {
  return CONFIGURABLE_MENU_KEYS.filter((key) =>
    key === toggledKey ? !visible : !isWorkspaceMenuVisible(settings, key),
  );
}
