"use client";

import type { Autopilot } from "@multica/core/types";
import {
  AUTOPILOT_SCOPES,
  type AutopilotColumnKey,
  type AutopilotListFilters,
  type AutopilotScope,
  type AutopilotSortDirection,
  type AutopilotSortField,
} from "@multica/core/autopilots/stores";
import { useActorName } from "@multica/core/workspace/hooks";
import {
  DropdownMenuCheckboxItem,
} from "@multica/ui/components/ui/dropdown-menu";
import { ActorAvatar } from "../../common/actor-avatar";
import { FILTER_ITEM_CLASS, HoverCheck } from "../../common/hover-check";
import {
  ToolbarCountBadge,
  ToolbarDisplaySettings,
  ToolbarFilterDropdown,
  ToolbarFilterSubmenu,
  ToolbarFrame,
  ToolbarResultCount,
  ToolbarScopeSelector,
} from "../../common/list-toolbar";
import { useT } from "../../i18n";

// Composite "type:id" value for polymorphic actor filter dimensions, so the
// string[] filter store can hold agent/squad/member references alike.
export function actorFilterValue(type: string, id: string): string {
  return `${type}:${id}`;
}

const COLUMN_KEYS: AutopilotColumnKey[] = [
  "assignee",
  "trigger",
  "lastRun",
  "nextRun",
  "mode",
  "creator",
  "created",
];

const SORT_FIELDS: AutopilotSortField[] = [
  "name",
  "lastRun",
  "nextRun",
  "created",
];

const MODES = ["create_issue", "run_only"] as const;
const TRIGGER_KINDS = ["schedule", "webhook", "api"] as const;

function countActiveFilterDimensions(filters: AutopilotListFilters): number {
  let count = 0;
  if (filters.assignees.length > 0) count++;
  if (filters.modes.length > 0) count++;
  if (filters.triggerKinds.length > 0) count++;
  if (filters.creators.length > 0) count++;
  return count;
}

export function AutopilotListToolbar({
  scope,
  onScopeChange,
  scopeCounts,
  filters,
  onToggleFilter,
  onClearFilters,
  sortField,
  sortDirection,
  onSortFieldChange,
  onSortDirectionChange,
  hiddenColumns,
  onToggleColumn,
  allRows,
  visibleCount,
}: {
  scope: AutopilotScope;
  onScopeChange: (scope: AutopilotScope) => void;
  /** Per-scope totals from the FULL set — scope counts ignore filters. */
  scopeCounts: Record<AutopilotScope, number>;
  filters: AutopilotListFilters;
  onToggleFilter: (key: keyof AutopilotListFilters, value: string) => void;
  onClearFilters: () => void;
  sortField: AutopilotSortField;
  sortDirection: AutopilotSortDirection;
  onSortFieldChange: (field: AutopilotSortField) => void;
  onSortDirectionChange: (direction: AutopilotSortDirection) => void;
  hiddenColumns: AutopilotColumnKey[];
  onToggleColumn: (key: AutopilotColumnKey) => void;
  /** Rows within the current scope, unfiltered — filter option lists and
   *  counts derive from this set. */
  allRows: Autopilot[];
  /** Rows surviving the filters — shown as "n / total" when narrowed. */
  visibleCount: number;
}) {
  const { t } = useT("autopilots");
  const { getActorName } = useActorName();

  const activeCount = countActiveFilterDimensions(filters);
  const hasActiveFilters = activeCount > 0;

  // Option lists with counts, derived from the scope's unfiltered rows so
  // toggling one dimension doesn't make the others' options vanish.
  const assigneeOptions = new Map<
    string,
    { type: string; id: string; count: number }
  >();
  const creatorOptions = new Map<
    string,
    { type: string; id: string; count: number }
  >();
  const modeCounts = new Map<string, number>();
  const triggerKindCounts = new Map<string, number>();
  for (const row of allRows) {
    const aKey = actorFilterValue(row.assignee_type, row.assignee_id);
    const a = assigneeOptions.get(aKey);
    if (a) a.count += 1;
    else
      assigneeOptions.set(aKey, {
        type: row.assignee_type,
        id: row.assignee_id,
        count: 1,
      });
    const cKey = actorFilterValue(row.created_by_type, row.created_by_id);
    const c = creatorOptions.get(cKey);
    if (c) c.count += 1;
    else
      creatorOptions.set(cKey, {
        type: row.created_by_type,
        id: row.created_by_id,
        count: 1,
      });
    modeCounts.set(
      row.execution_mode,
      (modeCounts.get(row.execution_mode) ?? 0) + 1,
    );
    for (const kind of row.trigger_kinds ?? []) {
      triggerKindCounts.set(kind, (triggerKindCounts.get(kind) ?? 0) + 1);
    }
  }

  const SCOPE_LABELS: Record<AutopilotScope, string> = {
    all: t(($) => $.page.scope_all),
    active: t(($) => $.status.active),
    paused: t(($) => $.status.paused),
  };

  const SORT_LABELS: Record<AutopilotSortField, string> = {
    name: t(($) => $.page.table.name),
    lastRun: t(($) => $.page.table.last_run),
    nextRun: t(($) => $.page.table.next_run),
    created: t(($) => $.page.table.created),
  };

  const COLUMN_LABELS: Record<AutopilotColumnKey, string> = {
    assignee: t(($) => $.page.table.assignee),
    trigger: t(($) => $.page.table.trigger),
    lastRun: t(($) => $.page.table.last_run),
    nextRun: t(($) => $.page.table.next_run),
    mode: t(($) => $.page.table.mode),
    creator: t(($) => $.page.table.created_by),
    created: t(($) => $.page.table.created),
  };

  return (
    <ToolbarFrame
      left={
        <>
          {/* Scope is the promoted status
          dimension (it does NOT appear in the filter dropdown). No search
          box: scope buttons already partition the (small) set, so search
          was dropped by product call. The count only appears while filters
          narrow the list. Button styling and the <md dropdown collapse
          follow the issues header's scope buttons. */}
          <ToolbarScopeSelector
            scopes={AUTOPILOT_SCOPES}
            scope={scope}
            scopeCounts={scopeCounts}
            scopeLabels={SCOPE_LABELS}
            onScopeChange={onScopeChange}
          />

          <ToolbarResultCount
            active={hasActiveFilters}
            title={t(($) => $.toolbar.result_count_title)}
            visibleCount={visibleCount}
            totalCount={allRows.length}
          />
        </>
      }
    >
      {/* Filter */}
      <ToolbarFilterDropdown
        hasActiveFilters={hasActiveFilters}
        activeCount={activeCount}
        activeLabel={t(($) => $.toolbar.filter_active_count, {
          count: activeCount,
        })}
        filterLabel={t(($) => $.toolbar.filter_label)}
        clearLabel={t(($) => $.toolbar.clear_filters)}
        onClearFilters={onClearFilters}
      >
        {/* Assignee */}
        <ToolbarFilterSubmenu
          label={t(($) => $.toolbar.section_assignee)}
          selectedCount={filters.assignees.length}
          contentClassName="max-h-72 w-auto min-w-48 overflow-y-auto"
        >
          {[...assigneeOptions.entries()].map(
            ([value, { type, id, count }]) => (
              <DropdownMenuCheckboxItem
                key={value}
                checked={filters.assignees.includes(value)}
                onCheckedChange={() => onToggleFilter("assignees", value)}
                className={FILTER_ITEM_CLASS}
              >
                <HoverCheck checked={filters.assignees.includes(value)} />
                <ActorAvatar actorType={type} actorId={id} size={16} />
                <span className="min-w-0 truncate">
                  {getActorName(type, id)}
                </span>
                <ToolbarCountBadge count={count} />
              </DropdownMenuCheckboxItem>
            ),
          )}
        </ToolbarFilterSubmenu>

          {/* Trigger kind */}
        <ToolbarFilterSubmenu
          label={t(($) => $.toolbar.section_trigger)}
          selectedCount={filters.triggerKinds.length}
        >
          {TRIGGER_KINDS.filter((kind) => triggerKindCounts.has(kind)).map(
            (kind) => (
              <DropdownMenuCheckboxItem
                key={kind}
                checked={filters.triggerKinds.includes(kind)}
                onCheckedChange={() => onToggleFilter("triggerKinds", kind)}
                className={FILTER_ITEM_CLASS}
              >
                <HoverCheck checked={filters.triggerKinds.includes(kind)} />
                {t(($) => $.trigger_kind[kind])}
                <ToolbarCountBadge count={triggerKindCounts.get(kind) ?? 0} />
              </DropdownMenuCheckboxItem>
            ),
          )}
        </ToolbarFilterSubmenu>

          {/* Mode */}
        <ToolbarFilterSubmenu
          label={t(($) => $.toolbar.section_mode)}
          selectedCount={filters.modes.length}
        >
          {MODES.map((mode) => (
            <DropdownMenuCheckboxItem
              key={mode}
              checked={filters.modes.includes(mode)}
              onCheckedChange={() => onToggleFilter("modes", mode)}
              className={FILTER_ITEM_CLASS}
            >
              <HoverCheck checked={filters.modes.includes(mode)} />
              {t(($) => $.execution_mode[mode])}
              <ToolbarCountBadge count={modeCounts.get(mode) ?? 0} />
            </DropdownMenuCheckboxItem>
          ))}
        </ToolbarFilterSubmenu>

          {/* Creator */}
        <ToolbarFilterSubmenu
          label={t(($) => $.toolbar.section_creator)}
          selectedCount={filters.creators.length}
          contentClassName="max-h-72 w-auto min-w-48 overflow-y-auto"
        >
          {[...creatorOptions.entries()].map(
            ([value, { type, id, count }]) => (
              <DropdownMenuCheckboxItem
                key={value}
                checked={filters.creators.includes(value)}
                onCheckedChange={() => onToggleFilter("creators", value)}
                className={FILTER_ITEM_CLASS}
              >
                <HoverCheck checked={filters.creators.includes(value)} />
                <ActorAvatar actorType={type} actorId={id} size={16} />
                <span className="min-w-0 truncate">
                  {getActorName(type, id)}
                </span>
                <ToolbarCountBadge count={count} />
              </DropdownMenuCheckboxItem>
            ),
          )}
        </ToolbarFilterSubmenu>
      </ToolbarFilterDropdown>

      <ToolbarDisplaySettings
        sortField={sortField}
        sortDirection={sortDirection}
        sortFields={SORT_FIELDS}
        sortLabels={SORT_LABELS}
        onSortFieldChange={onSortFieldChange}
        onSortDirectionChange={onSortDirectionChange}
        columnKeys={COLUMN_KEYS}
        columnLabels={COLUMN_LABELS}
        hiddenColumns={hiddenColumns}
        onToggleColumn={onToggleColumn}
        displayLabel={t(($) => $.toolbar.display)}
        sortByLabel={t(($) => $.toolbar.sort_by)}
        directionAscLabel={t(($) => $.toolbar.direction_asc)}
        directionDescLabel={t(($) => $.toolbar.direction_desc)}
        columnsLabel={t(($) => $.toolbar.section_columns)}
      />
    </ToolbarFrame>
  );
}
