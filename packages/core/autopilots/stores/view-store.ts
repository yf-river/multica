"use client";

import {
  createWorkspaceListViewStore,
  type WorkspaceListViewStoreState,
} from "../../platform/workspace-list-view-store";

// View preferences for the autopilots list page: scope, sort, column
// visibility, and filters. Persisted per workspace (workspace-aware storage),
// per user/device (localStorage). Search text and row selection are
// deliberately NOT stored — they are session-scoped (same rationale as the
// skills view store).

// Status is the promoted SCOPE dimension (lifecycle stage, mutually
// exclusive) — it therefore does NOT appear in `filters`; one dimension
// lives in exactly one place. "all" = active + paused. There is no
// archived scope because the current product has no UI archiving flow.
export type AutopilotScope = "all" | "active" | "paused";

export const AUTOPILOT_SCOPES: AutopilotScope[] = ["all", "active", "paused"];

export type AutopilotSortField = "name" | "lastRun" | "nextRun" | "created";

export type AutopilotSortDirection = "asc" | "desc";

/** Per-field direction applied when the user switches TO that field. */
export const AUTOPILOT_SORT_DEFAULT_DIRECTION: Record<
  AutopilotSortField,
  AutopilotSortDirection
> = {
  name: "asc",
  lastRun: "desc",
  nextRun: "asc",
  created: "desc",
};

/** Multi-select filter state. Empty array per dimension = inactive. */
export interface AutopilotListFilters {
  assignees: string[];
  modes: string[];
  triggerKinds: string[];
  creators: string[];
}

export const EMPTY_AUTOPILOT_FILTERS: AutopilotListFilters = {
  assignees: [],
  modes: [],
  triggerKinds: [],
  creators: [],
};

// User-hideable columns. Name and the structural columns (checkbox, kebab)
// are always visible.
export type AutopilotColumnKey =
  | "assignee"
  | "trigger"
  | "lastRun"
  | "nextRun"
  | "mode"
  | "creator"
  | "created";

/** Mode, creator and created are opt-in: hidden until the user enables them. */
export const AUTOPILOT_DEFAULT_HIDDEN_COLUMNS: AutopilotColumnKey[] = [
  "mode",
  "creator",
  "created",
];

interface AutopilotsViewActions {
  setScope: (scope: AutopilotScope) => void;
}

const DEFAULTS = {
  scope: "all" as AutopilotScope,
  sortField: "lastRun" as AutopilotSortField,
  sortDirection: AUTOPILOT_SORT_DEFAULT_DIRECTION.lastRun,
  hiddenColumns: AUTOPILOT_DEFAULT_HIDDEN_COLUMNS,
  filters: EMPTY_AUTOPILOT_FILTERS,
};

export type AutopilotsViewState = WorkspaceListViewStoreState<
  typeof DEFAULTS,
  AutopilotsViewActions
>;

export const useAutopilotsViewStore = createWorkspaceListViewStore<
  typeof DEFAULTS,
  AutopilotsViewActions
>({
  name: "multica_autopilots_view",
  defaults: DEFAULTS,
  sortDirections: AUTOPILOT_SORT_DEFAULT_DIRECTION,
  version: 1,
  createExtraActions: (set) => ({
    setScope: (scope) => set({ scope }),
  }),
});
