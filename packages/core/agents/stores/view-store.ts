"use client";

import { createWorkspaceListViewStore } from "../../platform/workspace-list-view-store";

// View preferences for the agents list page: scope, sort, column visibility,
// and filters. Persisted per workspace, per user/device. Row selection is
// session-scoped on purpose (same rationale as the skills/autopilots view
// stores).

// Scope combines the ownership lens with the archived lifecycle stage. The
// three values are mutually exclusive, and archived intentionally ignores
// ownership so it does not hide other members' archived agents.
export type AgentsScope = "mine" | "all" | "archived";

export const AGENT_SCOPES: AgentsScope[] = ["mine", "all", "archived"];

export type AgentSortField = "lastActive" | "name" | "runs" | "created";

export type AgentSortDirection = "asc" | "desc";

/** Per-field direction applied when the user switches TO that field. */
const AGENT_SORT_DEFAULT_DIRECTION: Record<
  AgentSortField,
  AgentSortDirection
> = {
  lastActive: "desc",
  name: "asc",
  runs: "desc",
  created: "desc",
};

/** Multi-select filter state. Empty array per dimension = inactive. */
export interface AgentListFilters {
  /** AgentAvailability values (online / unstable / offline). */
  availability: string[];
  /** Runtime ids. */
  runtimes: string[];
  /** Owner user ids. Owner is the same person-axis as the Mine scope: the
   *  "mine" scope is the clean no-filter personal view, and applying any
   *  filter (owner or otherwise) leaves it for "all" — see setScope /
   *  toggleFilter. So owner-as-filter and Mine never coexist, which keeps
   *  the axis orthogonal (no "mine + owner=someone-else = empty" state). */
  owners: string[];
  /** Runtime-native model identifiers (e.g. claude / codex / gpt-…). */
  models: string[];
}

const EMPTY_AGENT_FILTERS: AgentListFilters = {
  availability: [],
  runtimes: [],
  owners: [],
  models: [],
};

// User-hideable columns. Name and the structural columns (checkbox, kebab)
// are always visible.
export type AgentColumnKey =
  | "status"
  | "owner"
  | "runtime"
  | "lastActive"
  | "runs"
  | "model"
  | "created";

/** Model and created are opt-in: hidden until the user enables them. Owner
 *  is shown by default (the user wants to see who owns each agent). */
export const AGENT_DEFAULT_HIDDEN_COLUMNS: AgentColumnKey[] = [
  "model",
  "created",
];

interface AgentsViewActions {
  setScope: (scope: AgentsScope) => void;
}

const DEFAULTS = {
  // Most members start with their own agents; admins can switch to all.
  scope: "mine" as AgentsScope,
  sortField: "lastActive" as AgentSortField,
  sortDirection: AGENT_SORT_DEFAULT_DIRECTION.lastActive,
  hiddenColumns: AGENT_DEFAULT_HIDDEN_COLUMNS,
  filters: EMPTY_AGENT_FILTERS,
};

export const useAgentsViewStore = createWorkspaceListViewStore<
  typeof DEFAULTS,
  AgentsViewActions
>({
  name: "multica_agents_view",
  defaults: DEFAULTS,
  sortDirections: AGENT_SORT_DEFAULT_DIRECTION,
  version: 1,
  // "Mine" is the clean personal view. Entering it clears all filters, and
  // applying a filter leaves it for "all". Archived keeps its filters.
  createExtraActions: (set) => ({
    setScope: (scope) =>
      set(scope === "mine" ? { scope, filters: EMPTY_AGENT_FILTERS } : { scope }),
  }),
  afterFilterToggle: (state) =>
    state.scope === "mine" ? { scope: "all" } : {},
});
