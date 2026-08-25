"use client";

import { createWorkspaceListViewStore } from "../../platform/workspace-list-view-store";

// View preferences for the squads list page: scope, sort, column visibility.
// Persisted per workspace, per user/device. No filters (the set is tiny);
// no search (scope-bearing list).

// Scope mixes the ownership lens (creator-based) with the archived lifecycle
// stage, matching the agents list.
export type SquadsScope = "mine" | "all" | "archived";

export const SQUAD_SCOPES: SquadsScope[] = ["mine", "all", "archived"];

export type SquadSortField = "name" | "members" | "created";

export type SquadSortDirection = "asc" | "desc";

/** Per-field direction applied when the user switches TO that field. */
const SQUAD_SORT_DEFAULT_DIRECTION: Record<
  SquadSortField,
  SquadSortDirection
> = {
  name: "asc",
  members: "desc",
  created: "desc",
};

// User-hideable columns. Name and leader (the squad's defining relationship)
// are always visible.
export type SquadColumnKey = "members" | "creator" | "created";

/** Created (date) is opt-in. Creator ("Created by") is shown by default —
 *  the user wants to see who made each squad. Note it's "Created by", NOT
 *  "Owner": the squad creator holds no management rights (archiving is
 *  workspace-admin only), so labelling it Owner would mislead. */
export const SQUAD_DEFAULT_HIDDEN_COLUMNS: SquadColumnKey[] = ["created"];

/** Multi-select filters — the categorical columns (leader, creator). Empty
 *  array per dimension = inactive. */
export interface SquadListFilters {
  /** Leader agent ids. */
  leaders: string[];
  /** Creator member user ids. */
  creators: string[];
}

const EMPTY_SQUAD_FILTERS: SquadListFilters = {
  leaders: [],
  creators: [],
};

interface SquadsViewActions {
  setScope: (scope: SquadsScope) => void;
}

const DEFAULTS = {
  scope: "mine" as SquadsScope,
  sortField: "name" as SquadSortField,
  sortDirection: SQUAD_SORT_DEFAULT_DIRECTION.name,
  hiddenColumns: SQUAD_DEFAULT_HIDDEN_COLUMNS,
  filters: EMPTY_SQUAD_FILTERS,
};

export const useSquadsViewStore = createWorkspaceListViewStore<
  typeof DEFAULTS,
  SquadsViewActions
>({
  name: "multica_squads_view",
  defaults: DEFAULTS,
  sortDirections: SQUAD_SORT_DEFAULT_DIRECTION,
  createExtraActions: (set) => ({
    setScope: (scope) => set({ scope }),
  }),
});
