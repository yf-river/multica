// autopilot 列表页的工具栏：左侧范围切换（全部/运行中/已暂停 + 计数），右侧过滤
// （执行者/触发方式/运行模式/创建者）、显示设置（排序字段/方向/列显隐）。过滤与
// 排序状态存于共享 store，列表排序也写同一份。执行者与创建者是"类型:ID"复合值，
// 让 string[] 过滤器可统一容纳智能体/小队/成员引用。

"use client";

import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  Filter,
  X,
} from "lucide-react";
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
import { ActorAvatar } from "../../common/actor-avatar";
import { FILTER_ITEM_CLASS, HoverCheck } from "../../common/hover-check";
import { useT } from "../../i18n";
import { PAGE_TOOLBAR } from "../../layout/page-header";

/** 多态"执行者"过滤维度的复合 "类型:ID" 值，让 string[] 过滤器 store 能同存
 *  智能体/小队/成员引用。 */
export function actorFilterValue(type: string, id: string): string {
  return `${type}:${id}`;
}

// 可切换显示的列（与表头一致）。
const COLUMN_KEYS: AutopilotColumnKey[] = [
  "assignee",
  "trigger",
  "lastRun",
  "nextRun",
  "mode",
  "creator",
  "created",
];

// 可排序字段。
const SORT_FIELDS: AutopilotSortField[] = [
  "name",
  "lastRun",
  "nextRun",
  "created",
];

// 运行模式与触发方式的可选项。
const MODES = ["create_issue", "run_only"] as const;
const TRIGGER_KINDS = ["schedule", "webhook", "api"] as const;

/** 统计当前生效的过滤维度数（有几个维度选了值）。 */
export function countActiveFilterDimensions(
  filters: AutopilotListFilters,
): number {
  let count = 0;
  if (filters.assignees.length > 0) count++;
  if (filters.modes.length > 0) count++;
  if (filters.triggerKinds.length > 0) count++;
  if (filters.creators.length > 0) count++;
  return count;
}

/**
 * autopilot 列表工具栏。左侧范围按钮 + 计数；右侧过滤下拉（各维度可复选、带数量
 * 徽标与悬停勾选）与显示设置 popover（排序字段/方向、列显隐开关）。
 * @param scope 当前范围。
 * @param onScopeChange 范围切换回调。
 * @param scopeCounts 各范围总数——来自全集，范围计数忽略过滤。
 * @param filters 当前过滤条件。
 * @param onToggleFilter 切换某维度的某个值的回调。
 * @param onClearFilters 清空所有过滤回调。
 * @param sortField 当前排序字段。
 * @param sortDirection 当前排序方向。
 * @param onSortFieldChange 排序字段回调。
 * @param onSortDirectionChange 排序方向回调。
 * @param hiddenColumns 隐藏的列。
 * @param onToggleColumn 切换某列显隐。
 * @param allRows 当前范围内未过滤的行——过滤选项列表与计数都从这个集合推出。
 * @param visibleCount 经过过滤后存活的行的数量——收窄时显示为 "n / total"。
 */
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
  /** 各范围总数——来自全集，范围计数忽略过滤。 */
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
  /** 当前范围内未过滤的行——过滤选项列表与计数都由它推出。 */
  allRows: Autopilot[];
  /** 过滤后存活的行的数量——收窄时显示 "n / total"。 */
  visibleCount: number;
}) {
  const { t } = useT("autopilots");
  const { getActorName } = useActorName();

  const activeCount = countActiveFilterDimensions(filters);
  const hasActiveFilters = activeCount > 0;

  // 带计数的选项列表，从当前范围未过滤的行推出——这样切换一个维度不会让别的维度
  // 的选项消失。
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
  const sortLabel = SORT_LABELS[sortField];

  const countBadge = (n: number) => (
    <span className="ml-auto pl-3 text-caption text-muted-foreground">{n}</span>
  );

  return (
    <div className={PAGE_TOOLBAR}>
      {/* 左侧：范围按钮 + 结果计数。范围是被提升的状态维度（它不出现在过滤下拉里）。
          没有搜索框：范围按钮已把（小规模的）集合切分完毕，搜索是产品裁定砍掉的。
          计数只在过滤收窄列表时出现。按钮样式与 <md 下拉折叠沿用 issues 头部的
          范围按钮。 */}
      <div className="flex min-w-0 items-center gap-2">
        <div className="hidden shrink-0 items-center gap-1 md:flex">
          {AUTOPILOT_SCOPES.map((s) => (
            <Button
              key={s}
              variant="outline"
              size="sm"
              className={
                scope === s
                  ? "gap-1.5 bg-accent text-accent-foreground hover:bg-accent/80"
                  : "gap-1.5 text-muted-foreground"
              }
              onClick={() => onScopeChange(s)}
            >
              {SCOPE_LABELS[s]}
              <span className="tabular-nums text-caption text-muted-foreground">
                {scopeCounts[s]}
              </span>
            </Button>
          ))}
        </div>

        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="outline"
                size="sm"
                className="shrink-0 gap-1 text-muted-foreground md:hidden"
              >
                <span className="truncate">{SCOPE_LABELS[scope]}</span>
                <ChevronDown className="size-3 text-muted-foreground" />
              </Button>
            }
          />
          <DropdownMenuContent align="start" className="w-auto">
            <DropdownMenuRadioGroup
              value={scope}
              onValueChange={(value) =>
                onScopeChange(value as AutopilotScope)
              }
            >
              {AUTOPILOT_SCOPES.map((s) => (
                <DropdownMenuRadioItem key={s} value={s}>
                  {SCOPE_LABELS[s]}
                  <span className="ml-2 tabular-nums text-caption text-muted-foreground">
                    {scopeCounts[s]}
                  </span>
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>

        {hasActiveFilters && (
          <span
            title={t(($) => $.toolbar.result_count_title)}
            className="hidden shrink-0 text-caption tabular-nums text-muted-foreground md:inline"
          >
            {visibleCount} / {allRows.length}
          </span>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-1">
        {/* 过滤 */}
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
            {/* 执行者 */}
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <span className="flex-1">
                  {t(($) => $.toolbar.section_assignee)}
                </span>
                {filters.assignees.length > 0 && (
                  <span className="text-caption font-medium text-primary">
                    {filters.assignees.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="max-h-72 w-auto min-w-48 overflow-y-auto">
                {[...assigneeOptions.entries()].map(
                  ([value, { type, id, count }]) => (
                    <DropdownMenuCheckboxItem
                      key={value}
                      checked={filters.assignees.includes(value)}
                      onCheckedChange={() =>
                        onToggleFilter("assignees", value)
                      }
                      className={FILTER_ITEM_CLASS}
                    >
                      <HoverCheck
                        checked={filters.assignees.includes(value)}
                      />
                      <ActorAvatar actorType={type} actorId={id} size="sm" />
                      <span className="min-w-0 truncate">
                        {getActorName(type, id)}
                      </span>
                      {countBadge(count)}
                    </DropdownMenuCheckboxItem>
                  ),
                )}
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {/* 触发方式 */}
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <span className="flex-1">
                  {t(($) => $.toolbar.section_trigger)}
                </span>
                {filters.triggerKinds.length > 0 && (
                  <span className="text-caption font-medium text-primary">
                    {filters.triggerKinds.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-auto min-w-44">
                {TRIGGER_KINDS.filter((kind) =>
                  triggerKindCounts.has(kind),
                ).map((kind) => (
                  <DropdownMenuCheckboxItem
                    key={kind}
                    checked={filters.triggerKinds.includes(kind)}
                    onCheckedChange={() =>
                      onToggleFilter("triggerKinds", kind)
                    }
                    className={FILTER_ITEM_CLASS}
                  >
                    <HoverCheck
                      checked={filters.triggerKinds.includes(kind)}
                    />
                    {t(($) => $.trigger_kind[kind])}
                    {countBadge(triggerKindCounts.get(kind) ?? 0)}
                  </DropdownMenuCheckboxItem>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {/* 运行模式 */}
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <span className="flex-1">
                  {t(($) => $.toolbar.section_mode)}
                </span>
                {filters.modes.length > 0 && (
                  <span className="text-caption font-medium text-primary">
                    {filters.modes.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-auto min-w-44">
                {MODES.map((mode) => (
                  <DropdownMenuCheckboxItem
                    key={mode}
                    checked={filters.modes.includes(mode)}
                    onCheckedChange={() => onToggleFilter("modes", mode)}
                    className={FILTER_ITEM_CLASS}
                  >
                    <HoverCheck checked={filters.modes.includes(mode)} />
                    {t(($) => $.execution_mode[mode])}
                    {countBadge(modeCounts.get(mode) ?? 0)}
                  </DropdownMenuCheckboxItem>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {/* 创建者 */}
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <span className="flex-1">
                  {t(($) => $.toolbar.section_creator)}
                </span>
                {filters.creators.length > 0 && (
                  <span className="text-caption font-medium text-primary">
                    {filters.creators.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="max-h-72 w-auto min-w-48 overflow-y-auto">
                {[...creatorOptions.entries()].map(
                  ([value, { type, id, count }]) => (
                    <DropdownMenuCheckboxItem
                      key={value}
                      checked={filters.creators.includes(value)}
                      onCheckedChange={() => onToggleFilter("creators", value)}
                      className={FILTER_ITEM_CLASS}
                    >
                      <HoverCheck checked={filters.creators.includes(value)} />
                      <ActorAvatar actorType={type} actorId={id} size="sm" />
                      <span className="min-w-0 truncate">
                        {getActorName(type, id)}
                      </span>
                      {countBadge(count)}
                    </DropdownMenuCheckboxItem>
                  ),
                )}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
          </DropdownMenuContent>
        </DropdownMenu>

        {/* 显示设置——与技能同范式：排序字段选择 + 方向切换 + 列显隐开关，写的是
            列表表头排序按钮也在用的同一个 store。 */}
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
                        onSortFieldChange(v as AutopilotSortField)
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
