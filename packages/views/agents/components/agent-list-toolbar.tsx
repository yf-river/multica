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
  DropdownMenuCheckboxItem,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  ToolbarCountBadge,
  ToolbarDisplaySettings,
  ToolbarFilterDropdown,
  ToolbarResultCount,
  ToolbarScopeSelector,
} from "../../common/list-toolbar";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { FILTER_ITEM_CLASS, HoverCheck } from "../../common/hover-check";
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

function countActiveFilterDimensions(filters: AgentListFilters): number {
  let count = 0;
  if (filters.availability.length > 0) count++;
  if (filters.runtimes.length > 0) count++;
  if (filters.owners.length > 0) count++;
  if (filters.models.length > 0) count++;
  return count;
}

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

  const activeCount = countActiveFilterDimensions(filters);
  const hasActiveFilters = activeCount > 0;

  // Option lists with counts, derived from the scope's unfiltered rows so
  // toggling one dimension doesn't make the others' options vanish.
  const availabilityCounts = new Map<string, number>();
  const runtimeOptions = new Map<string, { name: string; count: number }>();
  for (const row of allRows) {
    if (row.presence) {
      availabilityCounts.set(
        row.presence.availability,
        (availabilityCounts.get(row.presence.availability) ?? 0) + 1,
      );
    }
    const rt = row.runtime;
    if (rt) {
      const entry = runtimeOptions.get(rt.id);
      if (entry) entry.count += 1;
      else runtimeOptions.set(rt.id, { name: rt.name, count: 1 });
    }
  }

  // Owner options: members who own at least one agent in the current scope.
  const memberById = new Map(members.map((m) => [m.user_id, m]));
  const ownerCounts = new Map<string, number>();
  const modelCounts = new Map<string, number>();
  for (const row of allRows) {
    const oid = row.agent.owner_id;
    if (oid) ownerCounts.set(oid, (ownerCounts.get(oid) ?? 0) + 1);
    const model = row.agent.model;
    if (model) modelCounts.set(model, (modelCounts.get(model) ?? 0) + 1);
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
    <div className="flex h-12 shrink-0 items-center justify-between gap-2 px-5">
      {/* Left: scope buttons + result count. Scope mixes the ownership lens
          (mine/all) with the archived lifecycle stage; no search box (scope
          partitions the small set). Button styling and the <md dropdown
          collapse follow the issues header's scope buttons. */}
      <div className="flex min-w-0 items-center gap-2">
        <ToolbarScopeSelector
          scopes={AGENT_SCOPES}
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
      </div>

      <div className="flex shrink-0 items-center gap-1">
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
          {/* Availability */}
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <span className="flex-1">
                {t(($) => $.toolbar.section_availability)}
              </span>
              {filters.availability.length > 0 && (
                <span className="text-xs font-medium text-primary">
                  {filters.availability.length}
                </span>
              )}
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent className="w-auto min-w-44">
              {AVAILABILITY_VALUES.map((value) => {
                const visual = availabilityConfig[value];
                return (
                  <DropdownMenuCheckboxItem
                    key={value}
                    checked={filters.availability.includes(value)}
                    onCheckedChange={() =>
                      onToggleFilter("availability", value)
                    }
                    className={FILTER_ITEM_CLASS}
                  >
                    <HoverCheck
                      checked={filters.availability.includes(value)}
                    />
                    <span
                      className={`size-1.5 shrink-0 rounded-full ${visual.dotClass}`}
                    />
                    {t(($) => $.availability[value])}
                    <ToolbarCountBadge
                      count={availabilityCounts.get(value) ?? 0}
                    />
                  </DropdownMenuCheckboxItem>
                );
              })}
            </DropdownMenuSubContent>
          </DropdownMenuSub>

          {/* Runtime */}
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <span className="flex-1">
                {t(($) => $.toolbar.section_runtime)}
              </span>
              {filters.runtimes.length > 0 && (
                <span className="text-xs font-medium text-primary">
                  {filters.runtimes.length}
                </span>
              )}
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent className="max-h-72 w-auto min-w-48 overflow-y-auto">
              {[...runtimeOptions.entries()].map(([id, { name, count }]) => (
                <DropdownMenuCheckboxItem
                  key={id}
                  checked={filters.runtimes.includes(id)}
                  onCheckedChange={() => onToggleFilter("runtimes", id)}
                  className={FILTER_ITEM_CLASS}
                >
                  <HoverCheck checked={filters.runtimes.includes(id)} />
                  <span className="min-w-0 truncate">{name}</span>
                  <ToolbarCountBadge count={count} />
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuSubContent>
          </DropdownMenuSub>

          {/* Owner — the same person-axis as the Mine scope. Picking an
                owner here leaves the clean "mine" view for "all" (store
                rule), so Mine + owner never coexist. */}
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <span className="flex-1">
                {t(($) => $.toolbar.section_owner)}
              </span>
              {filters.owners.length > 0 && (
                <span className="text-xs font-medium text-primary">
                  {filters.owners.length}
                </span>
              )}
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent className="max-h-72 w-auto min-w-48 overflow-y-auto">
              {[...ownerCounts.entries()].map(([userId, count]) => {
                const m = memberById.get(userId);
                return (
                  <DropdownMenuCheckboxItem
                    key={userId}
                    checked={filters.owners.includes(userId)}
                    onCheckedChange={() => onToggleFilter("owners", userId)}
                    className={FILTER_ITEM_CLASS}
                  >
                    <HoverCheck checked={filters.owners.includes(userId)} />
                    <ActorAvatar
                      name={m?.name ?? userId.slice(0, 8)}
                      initials={(m?.name ?? "?").slice(0, 2).toUpperCase()}
                      avatarUrl={resolvePublicFileUrl(m?.avatar_url ?? null)}
                      size={16}
                    />
                    <span className="min-w-0 truncate">
                      {m?.name ?? userId.slice(0, 8)}
                    </span>
                    <ToolbarCountBadge count={count} />
                  </DropdownMenuCheckboxItem>
                );
              })}
            </DropdownMenuSubContent>
          </DropdownMenuSub>

          {/* Model — runtime-native model id (categorical column → filter) */}
          {modelCounts.size > 0 && (
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <span className="flex-1">
                  {t(($) => $.toolbar.section_model)}
                </span>
                {filters.models.length > 0 && (
                  <span className="text-xs font-medium text-primary">
                    {filters.models.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="max-h-72 w-auto min-w-44 overflow-y-auto">
                {[...modelCounts.entries()].map(([model, count]) => (
                  <DropdownMenuCheckboxItem
                    key={model}
                    checked={filters.models.includes(model)}
                    onCheckedChange={() => onToggleFilter("models", model)}
                    className={FILTER_ITEM_CLASS}
                  >
                    <HoverCheck checked={filters.models.includes(model)} />
                    <span className="min-w-0 truncate">{model}</span>
                    <ToolbarCountBadge count={count} />
                  </DropdownMenuCheckboxItem>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
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
          displayLabel={t(($) => $.toolbar.display)}
          sortByLabel={t(($) => $.toolbar.sort_by)}
          directionAscLabel={t(($) => $.toolbar.direction_asc)}
          directionDescLabel={t(($) => $.toolbar.direction_desc)}
          columnsLabel={t(($) => $.toolbar.section_columns)}
        />
      </div>
    </div>
  );
}
