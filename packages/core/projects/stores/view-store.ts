"use client";

import { createWorkspaceListViewStore } from "../../platform/workspace-list-view-store";

// Projects is the one dual-view list: a dense table (compact) and a card
// grid (comfortable), toggled by viewMode. Sort + filters feed both views;
// hiddenColumns only applies to the table. No scope (lead is optional and
// often an agent, so there's no strong personal axis; status is a 5-value
// lifecycle better expressed as a filter). Search stays session-local.
type ProjectViewMode = "compact" | "comfortable";

export type ProjectSortField = "name" | "priority" | "status" | "progress" | "created";

type ProjectSortDirection = "asc" | "desc";

const PROJECT_SORT_DEFAULT_DIRECTION: Record<
  ProjectSortField,
  ProjectSortDirection
> = {
  name: "asc",
  priority: "desc",
  status: "asc",
  progress: "desc",
  created: "desc",
};

/** Multi-select filters. Empty array per dimension = inactive. */
interface ProjectListFilters {
  /** ProjectStatus values. */
  statuses: string[];
  /** ProjectPriority values. */
  priorities: string[];
  /** Composite "type:id" lead refs (member or agent). */
  leads: string[];
}

const EMPTY_PROJECT_FILTERS: ProjectListFilters = {
  statuses: [],
  priorities: [],
  leads: [],
};

// Hideable table columns. Name + status are the always-visible core (status
// is the project's defining lifecycle field), so they're not in this set.
export type ProjectColumnKey = "priority" | "progress" | "lead" | "issues" | "created";

/** Issues count is opt-in; the rest show by default (matching the prior
 *  compact table). */
const PROJECT_DEFAULT_HIDDEN_COLUMNS: ProjectColumnKey[] = ["issues"];

interface ProjectViewActions {
  setViewMode: (mode: ProjectViewMode) => void;
}

const DEFAULTS = {
  viewMode: "compact" as ProjectViewMode,
  sortField: "created" as ProjectSortField,
  sortDirection: PROJECT_SORT_DEFAULT_DIRECTION.created,
  hiddenColumns: PROJECT_DEFAULT_HIDDEN_COLUMNS,
  filters: EMPTY_PROJECT_FILTERS,
};

export const useProjectViewStore = createWorkspaceListViewStore<
  typeof DEFAULTS,
  ProjectViewActions
>({
  name: "multica_projects_view",
  defaults: DEFAULTS,
  sortDirections: PROJECT_SORT_DEFAULT_DIRECTION,
  createExtraActions: (set) => ({
    setViewMode: (viewMode) => set({ viewMode }),
  }),
});
