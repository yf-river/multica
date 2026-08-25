"use client";

import { createWorkspaceListViewStore } from "../../platform/workspace-list-view-store";

// View preferences for the skills list page: sort, column visibility, and
// filters. Persisted per workspace (workspace-aware storage), per user/device
// (localStorage). Search text and row selection are deliberately NOT stored —
// they are session-scoped, and persisting them would greet returning users
// with an inexplicably narrowed list.

export type SkillSortField = "name" | "usedBy" | "updated" | "created";

export type SkillSortDirection = "asc" | "desc";

/** Per-field direction applied when the user switches TO that field. */
const SKILL_SORT_DEFAULT_DIRECTION: Record<
  SkillSortField,
  SkillSortDirection
> = {
  name: "asc",
  usedBy: "desc",
  updated: "desc",
  created: "desc",
};

export type SkillOriginType =
  | "manual"
  | "runtime_local"
  | "clawhub"
  | "skills_sh"
  | "github";

/** Multi-select filter state. Empty array per dimension = inactive. */
export interface SkillListFilters {
  usage: ("used" | "unused")[];
  origins: SkillOriginType[];
  agents: string[];
  creators: string[];
}

const EMPTY_SKILL_FILTERS: SkillListFilters = {
  usage: [],
  origins: [],
  agents: [],
  creators: [],
};

// User-hideable columns. Name and the structural columns (checkbox, kebab)
// are always visible.
export type SkillColumnKey =
  | "usedBy"
  | "source"
  | "creator"
  | "updated"
  | "created";

/** Source and created are opt-in: hidden until the user enables them. */
export const DEFAULT_HIDDEN_COLUMNS: SkillColumnKey[] = ["source", "created"];

const DEFAULTS = {
  sortField: "updated" as SkillSortField,
  sortDirection: SKILL_SORT_DEFAULT_DIRECTION.updated,
  hiddenColumns: DEFAULT_HIDDEN_COLUMNS,
  filters: EMPTY_SKILL_FILTERS,
};

export const useSkillsViewStore = createWorkspaceListViewStore({
  name: "multica_skills_view",
  defaults: DEFAULTS,
  sortDirections: SKILL_SORT_DEFAULT_DIRECTION,
});
