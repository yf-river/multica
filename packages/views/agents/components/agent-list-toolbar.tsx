"use client";

import type { AgentAvailability } from "@multica/core/agents";
import type { MemberWithUser } from "@multica/core/types";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import {
  AGENT_SCOPES,
  type AgentColumnKey,
  type AgentListFilters,
  type AgentsScope,
  type AgentSortDirection,
  type AgentSortField,
} from "@multica/core/agents/stores";
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
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { availabilityConfig } from "../presence";
import { useT } from "../../i18n";
import type { AgentListRow } from "./agents-page";

const COLUMN_KEYS: AgentColumnKey[] = [
  "status",
  "owner",
  "runtime",
  "lastActive",
  "runs",
  "model",
  "created",
];

const SORT_FIELDS: AgentSortField[] = ["lastActive", "name", "runs", "created"];

const AVAILABILITY_VALUES: AgentAvailability[] = [
  "online",
  "unstable",
  "offline",
];

export function AgentListToolbar({
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
  members,
  visibleCount,
}: {
  scope: AgentsScope;
  onScopeChange: (scope: AgentsScope) => void;
  /** Per-scope totals from the FULL set — scope counts ignore filters. */
  scopeCounts: Record<AgentsScope, number>;
  filters: AgentListFilters;
  onToggleFilter: (key: keyof AgentListFilters, value: string) => void;
  onClearFilters: () => void;
  sortField: AgentSortField;
  sortDirection: AgentSortDirection;
  onSortFieldChange: (field: AgentSortField) => void;
  onSortDirectionChange: (direction: AgentSortDirection) => void;
  hiddenColumns: AgentColumnKey[];
  onToggleColumn: (key: AgentColumnKey) => void;
  /** Rows within the current scope, unfiltered — filter option lists and
   *  counts derive from this set. */
  allRows: AgentListRow[];
  members: MemberWithUser[];
  /** Rows surviving the filters — shown as "n / total" when narrowed. */
  visibleCount: number;
}) {
  const { t } = useT("agents");

  const activeCount = countActiveFilters(filters);
  const hasActiveFilters = activeCount > 0;

  // Option lists with counts, derived from the scope's unfiltered rows so
  // toggling one dimension doesn't make the others' options vanish.
  const availabilityCounts = new Map<string, number>();
  const runtimeOptions = new Map<string, { name: string; count: number }>();
  for (const row of allRows) {
    if (row.presence) {
      incrementCount(availabilityCounts, row.presence.availability);
    }
    const rt = row.runtime;
    if (rt) {
      incrementCountedOption(runtimeOptions, rt.id, { name: rt.name });
    }
  }

  // Owner options: members who own at least one agent in the current scope.
  const memberById = new Map(members.map((m) => [m.user_id, m]));
  const ownerCounts = new Map<string, number>();
  const modelCounts = new Map<string, number>();
  for (const row of allRows) {
    const oid = row.agent.owner_id;
    if (oid) incrementCount(ownerCounts, oid);
    const model = row.agent.model;
    if (model) incrementCount(modelCounts, model);
  }

  const SCOPE_LABELS: Record<AgentsScope, string> = {
    mine: t(($) => $.scope.mine),
    all: t(($) => $.scope.all),
    archived: t(($) => $.scope.archived),
  };

  const SORT_LABELS: Record<AgentSortField, string> = {
    lastActive: t(($) => $.columns.last_active),
    name: t(($) => $.columns.agent),
    runs: t(($) => $.columns.runs),
    created: t(($) => $.columns.created),
  };

  const COLUMN_LABELS: Record<AgentColumnKey, string> = {
    status: t(($) => $.columns.status),
    owner: t(($) => $.columns.owner),
    runtime: t(($) => $.columns.runtime),
    lastActive: t(($) => $.columns.last_active),
    runs: t(($) => $.columns.runs),
    model: t(($) => $.columns.model),
    created: t(($) => $.columns.created),
  };

  return (
    <ToolbarFrame
      left={
        <ToolbarScopeAndResult
          scopes={AGENT_SCOPES}
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
        {/* Availability */}
        <ToolbarFilterSubmenu
          label={t(($) => $.toolbar.section_availability)}
          selectedCount={filters.availability.length}
        >
          {AVAILABILITY_VALUES.map((value) => {
            const visual = availabilityConfig[value];
            return (
              <ToolbarFilterOption
                key={value}
                checked={filters.availability.includes(value)}
                onToggle={() => onToggleFilter("availability", value)}
                count={availabilityCounts.get(value) ?? 0}
              >
                <span
                  className={`size-1.5 shrink-0 rounded-full ${visual.dotClass}`}
                />
                {t(($) => $.availability[value])}
              </ToolbarFilterOption>
            );
          })}
        </ToolbarFilterSubmenu>

        {/* Runtime */}
        <ToolbarFilterSubmenu
          label={t(($) => $.toolbar.section_runtime)}
          selectedCount={filters.runtimes.length}
          contentClassName="max-h-72 w-auto min-w-48 overflow-y-auto"
        >
          {[...runtimeOptions.entries()].map(([id, { name, count }]) => (
            <ToolbarFilterOption
              key={id}
              checked={filters.runtimes.includes(id)}
              onToggle={() => onToggleFilter("runtimes", id)}
              count={count}
            >
              <span className="min-w-0 truncate">{name}</span>
            </ToolbarFilterOption>
          ))}
        </ToolbarFilterSubmenu>

        {/* Owner — the same person-axis as the Mine scope. Picking an
            owner here leaves the clean "mine" view for "all" (store
            rule), so Mine + owner never coexist. */}
        <ToolbarFilterSubmenu
          label={t(($) => $.toolbar.section_owner)}
          selectedCount={filters.owners.length}
          contentClassName="max-h-72 w-auto min-w-48 overflow-y-auto"
        >
          {[...ownerCounts.entries()].map(([userId, count]) => {
            const m = memberById.get(userId);
            return (
              <ToolbarFilterOption
                key={userId}
                checked={filters.owners.includes(userId)}
                onToggle={() => onToggleFilter("owners", userId)}
                count={count}
              >
                <ActorAvatar
                  name={m?.name ?? userId.slice(0, 8)}
                  initials={(m?.name ?? "?").slice(0, 2).toUpperCase()}
                  avatarUrl={resolvePublicFileUrl(m?.avatar_url ?? null)}
                  size={16}
                />
                <span className="min-w-0 truncate">
                  {m?.name ?? userId.slice(0, 8)}
                </span>
              </ToolbarFilterOption>
            );
          })}
        </ToolbarFilterSubmenu>

        {/* Model — runtime-native model id (categorical column → filter) */}
        {modelCounts.size > 0 && (
          <ToolbarFilterSubmenu
            label={t(($) => $.toolbar.section_model)}
            selectedCount={filters.models.length}
            contentClassName="max-h-72 w-auto min-w-44 overflow-y-auto"
          >
            {[...modelCounts.entries()].map(([model, count]) => (
              <ToolbarFilterOption
                key={model}
                checked={filters.models.includes(model)}
                onToggle={() => onToggleFilter("models", model)}
                count={count}
              >
                <span className="min-w-0 truncate">{model}</span>
              </ToolbarFilterOption>
            ))}
          </ToolbarFilterSubmenu>
        )}
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
