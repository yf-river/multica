"use client";

import { Download, HardDrive, Pencil, Search } from "lucide-react";
import type { Agent, MemberWithUser } from "@multica/core/types";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { Input } from "@multica/ui/components/ui/input";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import {
  countActiveFilters,
  incrementCount,
  incrementCountedOption,
  ToolbarDisplaySettings,
  ToolbarFilterDropdown,
  ToolbarFilterOption,
  ToolbarFilterSubmenu,
  ToolbarFrame,
  ToolbarResultCount,
} from "../../common/list-toolbar";
import {
  type SkillColumnKey,
  type SkillListFilters,
  type SkillOriginType,
  type SkillSortDirection,
  type SkillSortField,
} from "@multica/core/skills/stores";
import { useT } from "../../i18n";
import type { SkillRow } from "./skills-page";

const COLUMN_KEYS: SkillColumnKey[] = [
  "usedBy",
  "source",
  "creator",
  "updated",
  "created",
];

const SORT_FIELDS: SkillSortField[] = ["name", "usedBy", "updated", "created"];

const ORIGIN_TYPES: SkillOriginType[] = [
  "manual",
  "runtime_local",
  "clawhub",
  "skills_sh",
  "github",
];

function originIcon(type: SkillOriginType) {
  if (type === "manual") return <Pencil className="size-3.5" />;
  if (type === "runtime_local") return <HardDrive className="size-3.5" />;
  return <Download className="size-3.5" />;
}

export function SkillListToolbar({
  search,
  onSearchChange,
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
  search: string;
  onSearchChange: (v: string) => void;
  filters: SkillListFilters;
  onToggleFilter: (key: keyof SkillListFilters, value: string) => void;
  onClearFilters: () => void;
  sortField: SkillSortField;
  sortDirection: SkillSortDirection;
  onSortFieldChange: (field: SkillSortField) => void;
  onSortDirectionChange: (direction: SkillSortDirection) => void;
  hiddenColumns: SkillColumnKey[];
  onToggleColumn: (key: SkillColumnKey) => void;
  /** Unfiltered rows — option lists and counts derive from the full set. */
  allRows: SkillRow[];
  /** Rows surviving search + filters — shown as "n / total" when narrowed. */
  visibleCount: number;
}) {
  const { t } = useT("skills");

  const activeCount = countActiveFilters(filters);
  const hasActiveFilters = activeCount > 0;

  const usedCount = allRows.filter((r) => r.agents.length > 0).length;
  const unusedCount = allRows.length - usedCount;

  const originCounts = new Map<SkillOriginType, number>();
  const agentOptions = new Map<string, { agent: Agent; count: number }>();
  const creatorOptions = new Map<
    string,
    { member: MemberWithUser; count: number }
  >();
  for (const row of allRows) {
    incrementCount(originCounts, row.originType);
    for (const agent of row.agents) {
      incrementCountedOption(agentOptions, agent.id, { agent });
    }
    const creator = row.creator;
    if (creator) {
      incrementCountedOption(creatorOptions, creator.user_id, {
        member: creator,
      });
    }
  }

  const ORIGIN_LABELS: Record<SkillOriginType, string> = {
    manual: t(($) => $.table.source_manual),
    runtime_local: t(($) => $.table.source_runtime_unknown),
    clawhub: t(($) => $.table.source_clawhub),
    skills_sh: t(($) => $.table.source_skills_sh),
    github: t(($) => $.table.source_github),
  };

  const COLUMN_LABELS: Record<SkillColumnKey, string> = {
    usedBy: t(($) => $.table.used_by),
    source: t(($) => $.table.source),
    creator: t(($) => $.table.created_by),
    updated: t(($) => $.table.updated),
    created: t(($) => $.table.created),
  };

  const SORT_LABELS: Record<SkillSortField, string> = {
    name: t(($) => $.table.name),
    usedBy: t(($) => $.table.used_by),
    updated: t(($) => $.table.updated),
    created: t(($) => $.table.created),
  };

  return (
    <ToolbarFrame
      left={
        <>
          <div className="relative hidden md:block">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={search}
              onChange={(e) => onSearchChange(e.target.value)}
              placeholder={t(($) => $.page.search_placeholder)}
              className="h-8 w-64 pl-8 text-sm"
            />
          </div>
          <ToolbarResultCount
            active={hasActiveFilters || search.trim().length > 0}
            title={t(($) => $.toolbar.result_count_title)}
            visibleCount={visibleCount}
            totalCount={allRows.length}
          />
        </>
      }
    >
      <ToolbarFilterDropdown
        hasActiveFilters={hasActiveFilters}
        activeCount={activeCount}
        onClearFilters={onClearFilters}
      >
        <ToolbarFilterSubmenu
          label={t(($) => $.toolbar.section_usage)}
          selectedCount={filters.usage.length}
        >
          {(["used", "unused"] as const).map((value) => (
            <ToolbarFilterOption
              key={value}
              checked={filters.usage.includes(value)}
              onToggle={() => onToggleFilter("usage", value)}
              count={value === "used" ? usedCount : unusedCount}
            >
              {value === "used"
                ? t(($) => $.page.scopes.used.label)
                : t(($) => $.page.scopes.unused.label)}
            </ToolbarFilterOption>
          ))}
        </ToolbarFilterSubmenu>

        <ToolbarFilterSubmenu
          label={t(($) => $.table.source)}
          selectedCount={filters.origins.length}
          contentClassName="w-auto min-w-48"
        >
          {ORIGIN_TYPES.filter((type) => originCounts.has(type)).map(
            (type) => (
              <ToolbarFilterOption
                key={type}
                checked={filters.origins.includes(type)}
                onToggle={() => onToggleFilter("origins", type)}
                count={originCounts.get(type) ?? 0}
              >
                {originIcon(type)}
                {ORIGIN_LABELS[type]}
              </ToolbarFilterOption>
            ),
          )}
        </ToolbarFilterSubmenu>

        <ToolbarFilterSubmenu
          label={t(($) => $.table.used_by)}
          selectedCount={filters.agents.length}
          contentClassName="max-h-72 w-auto min-w-48 overflow-y-auto"
        >
          {[...agentOptions.values()].map(({ agent, count }) => (
            <ToolbarFilterOption
              key={agent.id}
              checked={filters.agents.includes(agent.id)}
              onToggle={() => onToggleFilter("agents", agent.id)}
              count={count}
            >
              <ActorAvatar
                name={agent.name}
                initials={agent.name.slice(0, 2).toUpperCase()}
                avatarUrl={resolvePublicFileUrl(agent.avatar_url)}
                isAgent
                size={16}
              />
              <span className="min-w-0 truncate">{agent.name}</span>
            </ToolbarFilterOption>
          ))}
        </ToolbarFilterSubmenu>

        <ToolbarFilterSubmenu
          label={t(($) => $.table.created_by)}
          selectedCount={filters.creators.length}
          contentClassName="max-h-72 w-auto min-w-48 overflow-y-auto"
        >
          {[...creatorOptions.values()].map(({ member, count }) => (
            <ToolbarFilterOption
              key={member.user_id}
              checked={filters.creators.includes(member.user_id)}
              onToggle={() => onToggleFilter("creators", member.user_id)}
              count={count}
            >
              <ActorAvatar
                name={member.name}
                initials={member.name.slice(0, 2).toUpperCase()}
                avatarUrl={resolvePublicFileUrl(member.avatar_url)}
                size={16}
              />
              <span className="min-w-0 truncate">{member.name}</span>
            </ToolbarFilterOption>
          ))}
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
