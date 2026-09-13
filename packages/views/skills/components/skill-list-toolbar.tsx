// 本文件职责：技能列表页的工具栏——名称搜索、多维筛选（使用情况/来源/被哪些 agent 使用/
// 创建人）、排序字段与方向、列显隐，全部以「未过滤全集」推导选项和计数。
"use client";

import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  Download,
  Filter,
  HardDrive,
  Pencil,
  Search,
  X,
} from "lucide-react";
import type { Agent, MemberWithUser } from "@multica/core/types";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Input } from "@multica/ui/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { FILTER_ITEM_CLASS, HoverCheck } from "../../common/hover-check";
import {
  type SkillColumnKey,
  type SkillListFilters,
  type SkillOriginType,
  type SkillSortDirection,
  type SkillSortField,
} from "@multica/core/skills/stores";
import { useT } from "../../i18n";
import type { SkillRow } from "./skills-page";
import { PAGE_TOOLBAR } from "../../layout/page-header";

/** 技能来源类型（与核心 store 里的类型一致，此处的便捷别名）。 */
export type OriginType = SkillOriginType;

/** 可隐藏/显示的表格列（固定顺序）。 */
const COLUMN_KEYS: SkillColumnKey[] = [
  "usedBy",
  "source",
  "creator",
  "updated",
  "created",
];

/** 可排序的字段（固定顺序）。 */
const SORT_FIELDS: SkillSortField[] = ["name", "usedBy", "updated", "created"];

/** 统计当前有几个筛选维度生效（每个非空维度计 1）。 */
export function countActiveFilterDimensions(
  filters: SkillListFilters,
): number {
  let count = 0;
  if (filters.usage.length > 0) count++;
  if (filters.origins.length > 0) count++;
  if (filters.agents.length > 0) count++;
  if (filters.creators.length > 0) count++;
  return count;
}

/** 「来源」筛选里出现的全部类型（固定顺序）。 */
const ORIGIN_TYPES: OriginType[] = [
  "manual",
  "runtime_local",
  "clawhub",
  "skills_sh",
  "github",
];

/** 按来源类型挑一个小图标：手动创建用笔、本地 runtime 用硬盘、其余托管源用下载。 */
function originIcon(type: OriginType) {
  if (type === "manual") return <Pencil className="size-3.5" />;
  if (type === "runtime_local") return <HardDrive className="size-3.5" />;
  return <Download className="size-3.5" />;
}

/**
 * 技能列表页工具栏。
 *
 * 包含：名称搜索框（含「可见数/总数」角标）、「筛选」下拉（使用情况/来源/使用方 agent/
 * 创建人四组勾选项，各自带计数）、「显示设置」弹层（排序列 + 方向切换 + 列显隐开关）。
 * 排序/列显隐与列表头的操作共用同一份状态。
 */
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
  /** 未经过滤的全部行——选项列表与计数都从全集推导。 */
  allRows: SkillRow[];
  /** 经搜索 + 筛选后存活的行数——收窄时显示为「n / total」。 */
  visibleCount: number;
}) {
  const { t } = useT("skills");

  const activeCount = countActiveFilterDimensions(filters);
  const hasActiveFilters = activeCount > 0;

  // 带计数的选项列表，从「未过滤行」推导——这样切换某一维筛选时，其它维度的选项不会
  // 跟着消失。
  const usedCount = allRows.filter((r) => r.agents.length > 0).length;
  const unusedCount = allRows.length - usedCount;

  const originCounts = new Map<OriginType, number>();
  const agentOptions = new Map<string, { agent: Agent; count: number }>();
  const creatorOptions = new Map<
    string,
    { member: MemberWithUser; count: number }
  >();
  for (const row of allRows) {
    originCounts.set(row.originType, (originCounts.get(row.originType) ?? 0) + 1);
    for (const agent of row.agents) {
      const entry = agentOptions.get(agent.id);
      if (entry) entry.count += 1;
      else agentOptions.set(agent.id, { agent, count: 1 });
    }
    if (row.creator) {
      const entry = creatorOptions.get(row.creator.user_id);
      if (entry) entry.count += 1;
      else creatorOptions.set(row.creator.user_id, { member: row.creator, count: 1 });
    }
  }

  const ORIGIN_LABELS: Record<OriginType, string> = {
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
  const sortLabel = SORT_LABELS[sortField];

  const countBadge = (n: number) => (
    <span className="ml-auto pl-3 text-caption text-muted-foreground">{n}</span>
  );

  return (
    <div className={PAGE_TOOLBAR}>
      {/* 左侧：名称搜索 + 结果计数。计数只在搜索/筛选收窄列表时出现——空闲态下它
          只会重复页面头部已显示的总数。md 以下整个搜索框（及其计数）消失，沿用
          issues 头部的窄屏处理。 */}
      <div className="flex min-w-0 items-center gap-2">
        <div className="relative hidden md:block">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={search}
            onChange={(e) => onSearchChange(e.target.value)}
            aria-label={t(($) => $.page.search_placeholder)}
            placeholder={t(($) => $.page.search_placeholder)}
            className="h-8 w-64 pl-8 text-body"
          />
        </div>
        {(hasActiveFilters || search.trim().length > 0) && (
          <span
            title={t(($) => $.toolbar.result_count_title)}
            className="hidden shrink-0 text-caption tabular-nums text-muted-foreground md:inline"
          >
            {visibleCount} / {allRows.length}
          </span>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-1">
        {/* 筛选 */}
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant={hasActiveFilters ? "default" : "outline"}
                size="sm"
                className={
                  hasActiveFilters
                    ? "h-8 w-8 gap-1 bg-brand px-0 text-white hover:bg-brand/90 md:w-auto md:px-2.5"
                    : "h-8 w-8 gap-1 px-0 text-muted-foreground md:w-auto md:px-2.5"
                }
              >
                <Filter className="size-3.5" />
                {hasActiveFilters ? (
                  <>
                    <span className="hidden md:inline">
                      {t(($) => $.toolbar.filter_active_count, {
                        count: activeCount,
                      })}
                    </span>
                    <span className="tabular-nums md:hidden">
                      {activeCount}
                    </span>
                  </>
                ) : (
                  <span className="hidden md:inline">
                    {t(($) => $.toolbar.filter_label)}
                  </span>
                )}
                {hasActiveFilters && (
                  <span
                    role="button"
                    tabIndex={-1}
                    aria-label={t(($) => $.toolbar.clear_filters)}
                    className="-mr-1 ml-0.5 hidden rounded-sm p-0.5 hover:bg-white/20 md:inline-flex"
                    onClick={(e) => {
                      e.preventDefault();
                      e.stopPropagation();
                      onClearFilters();
                    }}
                    onPointerDown={(e) => e.stopPropagation()}
                  >
                    <X className="size-3" />
                  </span>
                )}
              </Button>
            }
          />
          <DropdownMenuContent align="end" className="w-auto">
            {/* 使用情况（已使用/未使用） */}
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <span className="flex-1">
                  {t(($) => $.toolbar.section_usage)}
                </span>
                {filters.usage.length > 0 && (
                  <span className="text-caption font-medium text-primary">
                    {filters.usage.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-auto min-w-44">
                {(["used", "unused"] as const).map((value) => (
                  <DropdownMenuCheckboxItem
                    key={value}
                    checked={filters.usage.includes(value)}
                    onCheckedChange={() => onToggleFilter("usage", value)}
                    className={FILTER_ITEM_CLASS}
                  >
                    <HoverCheck checked={filters.usage.includes(value)} />
                    {value === "used"
                      ? t(($) => $.page.scopes.used.label)
                      : t(($) => $.page.scopes.unused.label)}
                    {countBadge(value === "used" ? usedCount : unusedCount)}
                  </DropdownMenuCheckboxItem>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {/* 来源 */}
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <span className="flex-1">{t(($) => $.table.source)}</span>
                {filters.origins.length > 0 && (
                  <span className="text-caption font-medium text-primary">
                    {filters.origins.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-auto min-w-48">
                {ORIGIN_TYPES.filter((type) => originCounts.has(type)).map(
                  (type) => (
                    <DropdownMenuCheckboxItem
                      key={type}
                      checked={filters.origins.includes(type)}
                      onCheckedChange={() => onToggleFilter("origins", type)}
                      className={FILTER_ITEM_CLASS}
                    >
                      <HoverCheck checked={filters.origins.includes(type)} />
                      {originIcon(type)}
                      {ORIGIN_LABELS[type]}
                      {countBadge(originCounts.get(type) ?? 0)}
                    </DropdownMenuCheckboxItem>
                  ),
                )}
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {/* 被哪些 agent 使用 */}
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <span className="flex-1">{t(($) => $.table.used_by)}</span>
                {filters.agents.length > 0 && (
                  <span className="text-caption font-medium text-primary">
                    {filters.agents.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="max-h-72 w-auto min-w-48 overflow-y-auto">
                {[...agentOptions.values()].map(({ agent, count }) => (
                  <DropdownMenuCheckboxItem
                    key={agent.id}
                    checked={filters.agents.includes(agent.id)}
                    onCheckedChange={() => onToggleFilter("agents", agent.id)}
                    className={FILTER_ITEM_CLASS}
                  >
                    <HoverCheck checked={filters.agents.includes(agent.id)} />
                    <ActorAvatar
                      name={agent.name}
                      initials={agent.name.slice(0, 2).toUpperCase()}
                      avatarUrl={resolvePublicFileUrl(agent.avatar_url)}
                      isAgent
                      size="sm"
                    />
                    <span className="min-w-0 truncate">{agent.name}</span>
                    {countBadge(count)}
                  </DropdownMenuCheckboxItem>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {/* 创建人 */}
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <span className="flex-1">{t(($) => $.table.created_by)}</span>
                {filters.creators.length > 0 && (
                  <span className="text-caption font-medium text-primary">
                    {filters.creators.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="max-h-72 w-auto min-w-48 overflow-y-auto">
                {[...creatorOptions.values()].map(({ member, count }) => (
                  <DropdownMenuCheckboxItem
                    key={member.user_id}
                    checked={filters.creators.includes(member.user_id)}
                    onCheckedChange={() =>
                      onToggleFilter("creators", member.user_id)
                    }
                    className={FILTER_ITEM_CLASS}
                  >
                    <HoverCheck
                      checked={filters.creators.includes(member.user_id)}
                    />
                    <ActorAvatar
                      name={member.name}
                      initials={member.name.slice(0, 2).toUpperCase()}
                      avatarUrl={resolvePublicFileUrl(member.avatar_url)}
                      size="sm"
                    />
                    <span className="min-w-0 truncate">{member.name}</span>
                    {countBadge(count)}
                  </DropdownMenuCheckboxItem>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
          </DropdownMenuContent>
        </DropdownMenu>

        {/* 显示设置——与 issues 头部同一范式：Popover 内用边框分区，触发器展示当前
            排序（方向箭头 + 字段名），排序行是「嵌套选择器 + 方向切换按钮」，列开关为
            label + Switch 行。改动的是列表头按钮共用的同一对排序状态。 */}
        <Popover>
          <Tooltip>
            <PopoverTrigger
              render={
                <TooltipTrigger
                  render={
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-8 w-8 gap-1 px-0 text-muted-foreground md:w-auto md:px-2.5"
                    >
                      {sortDirection === "asc" ? (
                        <ArrowUp className="size-3.5" />
                      ) : (
                        <ArrowDown className="size-3.5" />
                      )}
                      <span className="hidden md:inline">{sortLabel}</span>
                    </Button>
                  }
                />
              }
            />
            <TooltipContent side="bottom">
              {t(($) => $.toolbar.display)}
            </TooltipContent>
          </Tooltip>
          <PopoverContent align="end" className="w-64 p-0">
            <div className="border-b px-3 py-2.5">
              <span className="text-caption font-medium text-muted-foreground">
                {t(($) => $.toolbar.sort_by)}
              </span>
              <div className="mt-2 flex items-center gap-1.5">
                <DropdownMenu>
                  <DropdownMenuTrigger
                    render={
                      <Button
                        variant="outline"
                        size="sm"
                        className="flex-1 justify-between text-caption"
                      >
                        {sortLabel}
                        <ChevronDown className="size-3 text-muted-foreground" />
                      </Button>
                    }
                  />
                  <DropdownMenuContent align="start" className="w-auto">
                    <DropdownMenuRadioGroup
                      value={sortField}
                      onValueChange={(v) =>
                        onSortFieldChange(v as SkillSortField)
                      }
                    >
                      {SORT_FIELDS.map((field) => (
                        <DropdownMenuRadioItem key={field} value={field}>
                          {SORT_LABELS[field]}
                        </DropdownMenuRadioItem>
                      ))}
                    </DropdownMenuRadioGroup>
                  </DropdownMenuContent>
                </DropdownMenu>
                <Button
                  variant="outline"
                  size="icon-sm"
                  onClick={() =>
                    onSortDirectionChange(
                      sortDirection === "asc" ? "desc" : "asc",
                    )
                  }
                  title={
                    sortDirection === "asc"
                      ? t(($) => $.toolbar.direction_asc)
                      : t(($) => $.toolbar.direction_desc)
                  }
                >
                  {sortDirection === "asc" ? (
                    <ArrowUp className="size-3.5" />
                  ) : (
                    <ArrowDown className="size-3.5" />
                  )}
                </Button>
              </div>
            </div>

            <div className="px-3 py-2.5">
              <span className="text-caption font-medium text-muted-foreground">
                {t(($) => $.toolbar.section_columns)}
              </span>
              <div className="mt-2 space-y-2">
                {COLUMN_KEYS.map((key) => (
                  <label
                    key={key}
                    className="flex cursor-pointer items-center justify-between"
                  >
                    <span className="text-body">{COLUMN_LABELS[key]}</span>
                    <Switch
                      size="sm"
                      checked={!hiddenColumns.includes(key)}
                      onCheckedChange={() => onToggleColumn(key)}
                    />
                  </label>
                ))}
              </div>
            </div>
          </PopoverContent>
        </Popover>
      </div>
    </div>
  );
}
