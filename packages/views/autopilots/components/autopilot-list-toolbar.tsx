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
import { ActorAvatar } from "../../common/actor-avatar";
import {
  countActiveFilters,
  incrementCount,
  incrementCountedOption,
  ToolbarDisplaySettings,
  ToolbarFilterDropdown,
  ToolbarFilterOption,
  ToolbarFilterSubmenu,
  ToolbarFrame,
  ToolbarScopeAndResult,
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
const TRIGGER_KINDS = ["schedule", "webhook"] as const;

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

  const activeCount = countActiveFilters(filters);
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
    incrementCountedOption(assigneeOptions, aKey, {
      type: row.assignee_type,
      id: row.assignee_id,
    });
    const cKey = actorFilterValue(row.created_by_type, row.created_by_id);
    incrementCountedOption(creatorOptions, cKey, {
      type: row.created_by_type,
      id: row.created_by_id,
    });
    incrementCount(modeCounts, row.execution_mode);
    for (const kind of row.trigger_kinds ?? []) {
      incrementCount(triggerKindCounts, kind);
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
        <ToolbarScopeAndResult
          scopes={AUTOPILOT_SCOPES}
          scope={scope}
          scopeCounts={scopeCounts}
          scopeLabels={SCOPE_LABELS}
          onScopeChange={onScopeChange}
          resultActive={hasActiveFilters}
          resultTitle={t(($) => $.toolbar.result_count_title)}
          visibleCount={visibleCount}
          totalCount={allRows.length}
        />
      }
    >
      {/* Filter */}
      <ToolbarFilterDropdown
        hasActiveFilters={hasActiveFilters}
        activeCount={activeCount}
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
              <ToolbarFilterOption
                key={value}
                checked={filters.assignees.includes(value)}
                onToggle={() => onToggleFilter("assignees", value)}
                count={count}
              >
                <ActorAvatar actorType={type} actorId={id} size={16} />
                <span className="min-w-0 truncate">
                  {getActorName(type, id)}
                </span>
              </ToolbarFilterOption>
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
              <ToolbarFilterOption
                key={kind}
                checked={filters.triggerKinds.includes(kind)}
                onToggle={() => onToggleFilter("triggerKinds", kind)}
                count={triggerKindCounts.get(kind) ?? 0}
              >
                {t(($) => $.trigger_kind[kind])}
              </ToolbarFilterOption>
            ),
          )}
        </ToolbarFilterSubmenu>

        {/* Mode */}
        <ToolbarFilterSubmenu
          label={t(($) => $.toolbar.section_mode)}
          selectedCount={filters.modes.length}
        >
          {MODES.map((mode) => (
            <ToolbarFilterOption
              key={mode}
              checked={filters.modes.includes(mode)}
              onToggle={() => onToggleFilter("modes", mode)}
              count={modeCounts.get(mode) ?? 0}
            >
              {t(($) => $.execution_mode[mode])}
            </ToolbarFilterOption>
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
              <ToolbarFilterOption
                key={value}
                checked={filters.creators.includes(value)}
                onToggle={() => onToggleFilter("creators", value)}
                count={count}
              >
                <ActorAvatar actorType={type} actorId={id} size={16} />
                <span className="min-w-0 truncate">
                  {getActorName(type, id)}
                </span>
              </ToolbarFilterOption>
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
        columnsLabel={t(($) => $.toolbar.section_columns)}
      />
    </ToolbarFrame>
  );
}
