"use client";

// 本文件：IssuesHeader —— 问题列表页的头部工具条，供全局 /issues 与项目详情页共用。
// 职责包括：视图选择栏（内置范围页签 + 已保存视图）、范围切换（全部/成员/代理）、
// 完整筛选菜单（状态/优先级/负责人/创建人/项目/标签/自定义属性/日期）、表格模式的
// 分组与列选择、显示设置抽屉（排序/分组/子问题/卡片属性）、视图切换菜单、
// "正在刷新"指示器，以及下方的"已激活筛选"chips 条与"另存为视图"对话框。

import { cloneElement, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowDown,
  ArrowUp,
  CalendarDays,
  ChartGantt,
  ChevronDown,
  CircleDot,
  Columns3,
  Filter,
  FolderKanban,
  FolderMinus,
  List,
  Rows3,
  SignalHigh,
  SlidersHorizontal,
  Tag,
  Table2,
  User,
  UserMinus,
  UserPen,
  Waves,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { Input } from "@multica/ui/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuCheckboxItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubTrigger,
  DropdownMenuSubContent,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import { Calendar } from "@multica/ui/components/ui/calendar";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectGroup,
  SelectItem,
} from "@multica/ui/components/ui/select";
import { Toggle } from "@multica/ui/components/ui/toggle";
import {
  PRIORITY_DISPLAY_ORDER,
} from "@multica/core/issues/config";
import { StatusIcon, PriorityIcon } from ".";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions, agentListOptions, squadListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import { labelListOptions } from "@multica/core/labels/queries";
import { propertyListOptions } from "@multica/core/properties";
import { propertyIdFromViewKey } from "@multica/core/issues/stores/view-store";
import type {
  Issue,
  IssueProperty,
  IssueTableFacetSpec,
  IssueTableFacetsResponse,
  WorkingAgentSummary,
} from "@multica/core/types";
import { formatActorRef, isActorPropertyType, isFilterablePropertyType, isScalarPropertyType, propertyFilterValueKey, PROPERTY_FILTER_OP_SYMBOLS, PROPERTY_FILTER_OPS_BY_TYPE, type PropertyFilterOp, type PropertyFilterValue } from "@multica/core/types";
import { ProjectIcon } from "../../projects/components/project-icon";
import { ActorAvatar } from "../../common/actor-avatar";
import { PropertyIcon } from "../../common/property-icon";
import { LabelChip } from "../../labels/label-chip";
import {
  SORT_OPTIONS,
  GROUPING_OPTIONS,
  SWIMLANE_GROUPINGS,
  CARD_PROPERTY_OPTIONS,
  type ActorFilterValue,
  type IssueDateField,
  type IssueDateFilter,
  type SortField,
  type IssueGrouping,
  type SwimlaneGrouping,
  type TableGrouping,
  type ViewMode,
} from "@multica/core/issues/stores/view-store";
import { useViewStore, useViewStoreApi } from "@multica/core/issues/stores/view-store-context";
import { FilterChipsBar } from "./filter-chips-bar";
import { SaveViewDialog, type SaveViewScope } from "./save-view-dialog";
import { ViewBar } from "./view-bar";
import { toast } from "sonner";
import { useActiveIssueView } from "@multica/core/issue-views/use-active-view";
import { useAuthStore } from "@multica/core/auth";
import type { IssueViewScope } from "@multica/core/issue-views/queries";
import { actorFilterKey, baselineFromQuery, type IssueViewBaseline } from "@multica/core/issue-views/baseline";
import type { IssueView } from "@multica/core/api/schemas";
import { addDaysDateOnly, dateOnlyToLocalDate, formatDateOnly, toDateOnly, todayDateOnly } from "@multica/core/issues/date";
import {
  useIssuesScope,
  useIssuesScopeStore,
  type IssuesScope,
  type IssuesScopePageKey,
} from "@multica/core/issues/stores/issues-scope-store";
import { actorKindForViewVariant } from "@multica/core/issues/surface/scope";
import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import { PAGE_GUTTER } from "../../layout/page-header";
import { useT } from "../../i18n";
import { useStatusOptions } from "../utils/status-options";
import { NO_PROPERTY_VALUE } from "../utils/filter";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { FILTER_ITEM_CLASS, HoverCheck } from "../../common/hover-check";
import { WorkspaceAgentWorkingChip } from "./workspace-agent-working-chip";
import { TableColumnPicker } from "./table-view";

type LocalDateRange = {
  from: Date | undefined;
  to?: Date;
};

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

function getActiveFilterCount(
  state: {
    statusFilters: string[];
    priorityFilters: string[];
    assigneeFilters: ActorFilterValue[];
    includeNoAssignee: boolean;
    creatorFilters: ActorFilterValue[];
    projectFilters: string[];
    includeNoProject: boolean;
    labelFilters: string[];
    propertyFilters?: Record<string, PropertyFilterValue[]>;
    dateFilter?: IssueDateFilter | null;
  },
  // 在已保存视图里，只有用户额外叠加在视图自身条件之上的增补才计入数量——
  // 基线是视图本身，而不是"当前是否在过滤"。
  baseline?: IssueViewBaseline,
) {
  let count = 0;
  const delta = (values: string[], fixed?: Set<string>) =>
    fixed ? values.filter((v) => !fixed.has(v)).length : values.length;
  if (delta(state.statusFilters, baseline?.status) > 0) count++;
  if (delta(state.priorityFilters, baseline?.priority) > 0) count++;
  const assigneeDelta =
    delta(state.assigneeFilters.map(actorFilterKey), baseline?.assignee) > 0 ||
    (state.includeNoAssignee && !(baseline?.includeNoAssignee ?? false));
  if (assigneeDelta) count++;
  if (delta(state.creatorFilters.map(actorFilterKey), baseline?.creator) > 0) count++;
  const projectDelta =
    delta(state.projectFilters, baseline?.project) > 0 ||
    (state.includeNoProject && !(baseline?.includeNoProject ?? false));
  if (projectDelta) count++;
  if (delta(state.labelFilters, baseline?.label) > 0) count++;
  for (const [id, selected] of Object.entries(state.propertyFilters ?? {})) {
    // 属性筛选值可能是操作符对象——统一用规范化键比较，这样视图已固定的
    // 操作符条件也能被正确抵消（不重复计数）。
    const fixed = baseline?.property.get(id);
    const deltaCount = fixed
      ? selected.filter((v) => !fixed.has(propertyFilterValueKey(v))).length
      : selected.length;
    if (deltaCount > 0) count++;
  }
  if (state.dateFilter) count++;
  return count;
}

function shortDateLabel(dateOnly: string) {
  return formatDateOnly(dateOnly, { month: "short", day: "numeric" }) || dateOnly;
}

function normalizeDateRange(from: Date, to: Date) {
  return from <= to ? [from, to] as const : [to, from] as const;
}

const DATE_FIELD_LABEL_KEY: Record<IssueDateField, "date_field_created" | "date_field_updated"> = {
  created_at: "date_field_created",
  updated_at: "date_field_updated",
};

/** 把它喂给 useIssueCounts，可以隐藏全部"按选项统计"的数字角标
 *  （角标只在计数 > 0 时渲染），同时不干扰选项列表本身。 */
const NO_COUNT_ISSUES: Issue[] = [];

function useIssueCounts(
  allIssues: Issue[],
  serverFacets?: IssueTableFacetsResponse,
) {
  return useMemo(() => {
    const status = new Map<string, number>();
    const priority = new Map<string, number>();
    const assignee = new Map<string, number>();
    const creator = new Map<string, number>();
    const project = new Map<string, number>();
    const label = new Map<string, number>();
    // 结构：属性定义 id → 选项 key → 数量。复选框的值按筛选 store 使用的
    // "true"/"false" 伪选项 key 计数。
    const property = new Map<string, Map<string, number>>();
    let noAssignee = 0;
    let noProject = 0;

    if (serverFacets) {
      for (const facet of serverFacets.facets) {
        const target =
          facet.kind === "status"
            ? status
            : facet.kind === "priority"
              ? priority
              : facet.kind === "assignee"
                ? assignee
                : facet.kind === "creator"
                  ? creator
                  : facet.kind === "project"
                    ? project
                    : facet.kind === "label"
                      ? label
                      : null;
        if (facet.kind === "property" && facet.property_id) {
          property.set(
            facet.property_id,
            new Map(facet.values.map((value) => [value.key, value.count])),
          );
          continue;
        }
        for (const value of facet.values) {
          if (facet.kind === "assignee" && value.key === "__none__") {
            noAssignee = value.count;
          } else if (facet.kind === "project" && value.key === "__none__") {
            noProject = value.count;
          } else {
            target?.set(value.key, value.count);
          }
        }
      }
      return { status, priority, assignee, creator, noAssignee, project, noProject, label, property };
    }

    for (const issue of allIssues) {
      status.set(issue.status, (status.get(issue.status) ?? 0) + 1);
      priority.set(issue.priority, (priority.get(issue.priority) ?? 0) + 1);

      if (!issue.assignee_id) {
        noAssignee++;
      } else {
        const aKey = `${issue.assignee_type}:${issue.assignee_id}`;
        assignee.set(aKey, (assignee.get(aKey) ?? 0) + 1);
      }

      const cKey = `${issue.creator_type}:${issue.creator_id}`;
      creator.set(cKey, (creator.get(cKey) ?? 0) + 1);

      if (!issue.project_id) {
        noProject++;
      } else {
        project.set(issue.project_id, (project.get(issue.project_id) ?? 0) + 1);
      }

      if (issue.labels) {
        for (const l of issue.labels) {
          label.set(l.id, (label.get(l.id) ?? 0) + 1);
        }
      }

      for (const [propertyId, value] of Object.entries(issue.properties ?? {})) {
        const optionKeys =
          typeof value === "string"
            ? [value]
            : Array.isArray(value)
              ? value
              : typeof value === "boolean"
                ? [String(value)]
                : [];
        if (optionKeys.length === 0) continue;
        let perOption = property.get(propertyId);
        if (!perOption) {
          perOption = new Map<string, number>();
          property.set(propertyId, perOption);
        }
        for (const key of optionKeys) {
          perOption.set(key, (perOption.get(key) ?? 0) + 1);
        }
      }
    }

    return { status, priority, assignee, creator, noAssignee, project, noProject, label, property };
  }, [allIssues, serverFacets]);
}

// ---------------------------------------------------------------------------
// 范围（Scope）配置：全部 / 成员 / 代理
// ---------------------------------------------------------------------------

const SCOPE_VALUES: IssuesScope[] = ["all", "members", "agents"];

// ---------------------------------------------------------------------------
// 成员（Actor）子菜单内容（负责人与创建人筛选共用）
// ---------------------------------------------------------------------------

function ActorSubContent({
  enabled,
  counts,
  selected,
  onToggle,
  showNoAssignee,
  includeNoAssignee,
  onToggleNoAssignee,
  noAssigneeCount,
  showSquads = true,
  fixedKeys,
  noAssigneeFixed = false,
  fixedTitle,
}: {
  enabled: boolean;
  counts: Map<string, number>;
  selected: ActorFilterValue[];
  onToggle: (value: ActorFilterValue) => void;
  showNoAssignee?: boolean;
  includeNoAssignee?: boolean;
  onToggleNoAssignee?: () => void;
  noAssigneeCount?: number;
  showSquads?: boolean;
  /** 被当前已打开保存视图固定的成员 key（形如 `type:id`），这类项要锁定勾选并禁用。 */
  fixedKeys?: Set<string>;
  noAssigneeFixed?: boolean;
  fixedTitle?: string;
}) {
  const { t } = useT("issues");
  const [search, setSearch] = useState("");
  const wsId = useWorkspaceId();
  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled,
  });
  const { data: agents = [] } = useQuery({
    ...agentListOptions(wsId),
    enabled,
  });
  const { data: squads = [] } = useQuery({
    ...squadListOptions(wsId),
    enabled,
  });
  const query = search.trim().toLowerCase();
  const filteredMembers = members.filter((m) =>
    m.name.toLowerCase().includes(query) || matchesPinyin(m.name, query),
  );
  const filteredAgents = agents.filter((a) =>
    !a.archived_at && (a.name.toLowerCase().includes(query) || matchesPinyin(a.name, query)),
  );
  const filteredSquads = squads.filter((s) =>
    !s.archived_at && (s.name.toLowerCase().includes(query) || matchesPinyin(s.name, query)),
  );

  const isSelected = (type: "member" | "agent" | "squad", id: string) =>
    selected.some((f) => f.type === type && f.id === id);

  return (
    <>
      <div className="px-2 py-1.5 border-b border-foreground/5">
        <input
          type="text"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t(($) => $.filters.placeholder)}
          className="w-full bg-transparent text-body placeholder:text-muted-foreground outline-none"
          autoFocus
        />
      </div>

      <div className="max-h-64 overflow-y-auto p-1">
        {showNoAssignee &&
          (!query || "no assignee".includes(query) || "unassigned".includes(query)) && (
            <DropdownMenuCheckboxItem
              checked={includeNoAssignee ?? false}
              disabled={noAssigneeFixed}
              title={noAssigneeFixed ? fixedTitle : undefined}
              onCheckedChange={() => onToggleNoAssignee?.()}
              className={FILTER_ITEM_CLASS}
            >
              <HoverCheck checked={includeNoAssignee ?? false} />
              <UserMinus className="size-3.5 text-muted-foreground" />
              {t(($) => $.filters.no_assignee)}
              {(noAssigneeCount ?? 0) > 0 && (
                <span className="ml-auto text-caption text-muted-foreground">
                  {noAssigneeCount}
                </span>
              )}
            </DropdownMenuCheckboxItem>
          )}

        {filteredMembers.length > 0 && (
          <DropdownMenuGroup>
            <DropdownMenuLabel>{t(($) => $.filters.members_group)}</DropdownMenuLabel>
            {filteredMembers.map((m) => {
              const checked = isSelected("member", m.user_id);
              const count = counts.get(`member:${m.user_id}`) ?? 0;
              return (
                <DropdownMenuCheckboxItem
                  key={m.user_id}
                  checked={checked}
                  disabled={fixedKeys?.has(`member:${m.user_id}`) === true}
                  title={fixedKeys?.has(`member:${m.user_id}`) === true ? fixedTitle : undefined}
                  onCheckedChange={() =>
                    onToggle({ type: "member", id: m.user_id })
                  }
                  className={FILTER_ITEM_CLASS}
                >
                  <HoverCheck checked={checked} />
                  <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
                  <span className="truncate">{m.name}</span>
                  {count > 0 && (
                    <span className="ml-auto text-caption text-muted-foreground">
                      {count}
                    </span>
                  )}
                </DropdownMenuCheckboxItem>
              );
            })}
          </DropdownMenuGroup>
        )}

        {filteredAgents.length > 0 && (
          <DropdownMenuGroup>
            <DropdownMenuLabel>{t(($) => $.filters.agents_group)}</DropdownMenuLabel>
            {filteredAgents.map((a) => {
              const checked = isSelected("agent", a.id);
              const count = counts.get(`agent:${a.id}`) ?? 0;
              return (
                <DropdownMenuCheckboxItem
                  key={a.id}
                  checked={checked}
                  disabled={fixedKeys?.has(`agent:${a.id}`) === true}
                  title={fixedKeys?.has(`agent:${a.id}`) === true ? fixedTitle : undefined}
                  onCheckedChange={() =>
                    onToggle({ type: "agent", id: a.id })
                  }
                  className={FILTER_ITEM_CLASS}
                >
                  <HoverCheck checked={checked} />
                  <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot />
                  <span className="truncate">{a.name}</span>
                  {count > 0 && (
                    <span className="ml-auto text-caption text-muted-foreground">
                      {count}
                    </span>
                  )}
                </DropdownMenuCheckboxItem>
              );
            })}
          </DropdownMenuGroup>
        )}

        {showSquads && filteredSquads.length > 0 && (
          <DropdownMenuGroup>
            <DropdownMenuLabel>{t(($) => $.filters.squads_group)}</DropdownMenuLabel>
            {filteredSquads.map((s) => {
              const checked = isSelected("squad", s.id);
              const count = counts.get(`squad:${s.id}`) ?? 0;
              return (
                <DropdownMenuCheckboxItem
                  key={s.id}
                  checked={checked}
                  disabled={fixedKeys?.has(`squad:${s.id}`) === true}
                  title={fixedKeys?.has(`squad:${s.id}`) === true ? fixedTitle : undefined}
                  onCheckedChange={() =>
                    onToggle({ type: "squad", id: s.id })
                  }
                  className={FILTER_ITEM_CLASS}
                >
                  <HoverCheck checked={checked} />
                  <ActorAvatar actorType="squad" actorId={s.id} size="sm" />
                  <span className="truncate">{s.name}</span>
                  {count > 0 && (
                    <span className="ml-auto text-caption text-muted-foreground">
                      {count}
                    </span>
                  )}
                </DropdownMenuCheckboxItem>
              );
            })}
          </DropdownMenuGroup>
        )}

        {filteredMembers.length === 0 && filteredAgents.length === 0 && (!showSquads || filteredSquads.length === 0) && search && (
          <div className="px-2 py-3 text-center text-body text-muted-foreground">
            {t(($) => $.filters.no_results)}
          </div>
        )}
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// 项目子菜单内容
// ---------------------------------------------------------------------------

function ProjectSubContent({
  enabled,
  counts,
  selected,
  onToggle,
  includeNoProject,
  onToggleNoProject,
  noProjectCount,
  fixedIds,
  noProjectFixed = false,
  fixedTitle,
}: {
  enabled: boolean;
  counts: Map<string, number>;
  selected: string[];
  onToggle: (projectId: string) => void;
  includeNoProject: boolean;
  onToggleNoProject: () => void;
  noProjectCount: number;
  fixedIds?: Set<string>;
  noProjectFixed?: boolean;
  fixedTitle?: string;
}) {
  const { t } = useT("issues");
  const [search, setSearch] = useState("");
  const wsId = useWorkspaceId();
  const { data: projects = [] } = useQuery({
    ...projectListOptions(wsId),
    enabled,
  });
  const query = search.trim().toLowerCase();
  const filtered = projects.filter((p) =>
    p.title.toLowerCase().includes(query),
  );

  return (
    <>
      <div className="px-2 py-1.5 border-b border-foreground/5">
        <input
          type="text"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t(($) => $.filters.placeholder)}
          className="w-full bg-transparent text-body placeholder:text-muted-foreground outline-none"
          autoFocus
        />
      </div>

      <div className="max-h-64 overflow-y-auto p-1">
        {(!query || "no project".includes(query) || "unassigned".includes(query)) && (
          <DropdownMenuCheckboxItem
            checked={includeNoProject}
            disabled={noProjectFixed}
            title={noProjectFixed ? fixedTitle : undefined}
            onCheckedChange={() => onToggleNoProject()}
            className={FILTER_ITEM_CLASS}
          >
            <HoverCheck checked={includeNoProject} />
            <FolderMinus className="size-3.5 text-muted-foreground" />
            {t(($) => $.filters.no_project)}
            {noProjectCount > 0 && (
              <span className="ml-auto text-caption text-muted-foreground">
                {noProjectCount}
              </span>
            )}
          </DropdownMenuCheckboxItem>
        )}

        {filtered.map((p) => {
          const checked = selected.includes(p.id);
          const count = counts.get(p.id) ?? 0;
          return (
            <DropdownMenuCheckboxItem
              key={p.id}
              checked={checked}
              disabled={fixedIds?.has(p.id) === true}
              title={fixedIds?.has(p.id) === true ? fixedTitle : undefined}
              onCheckedChange={() => onToggle(p.id)}
              className={FILTER_ITEM_CLASS}
            >
              <HoverCheck checked={checked} />
              <ProjectIcon project={p} size="sm" />
              <span className="truncate">{p.title}</span>
              {count > 0 && (
                <span className="ml-auto text-caption text-muted-foreground">
                  {count}
                </span>
              )}
            </DropdownMenuCheckboxItem>
          );
        })}

        {filtered.length === 0 && search && (
          <div className="px-2 py-3 text-center text-body text-muted-foreground">
            {t(($) => $.filters.no_results)}
          </div>
        )}
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// 标签子菜单内容
// ---------------------------------------------------------------------------

function LabelSubContent({
  enabled,
  counts,
  selected,
  onToggle,
  fixedIds,
  fixedTitle,
}: {
  enabled: boolean;
  counts: Map<string, number>;
  selected: string[];
  onToggle: (labelId: string) => void;
  fixedIds?: Set<string>;
  fixedTitle?: string;
}) {
  const { t } = useT("issues");
  const [search, setSearch] = useState("");
  const wsId = useWorkspaceId();
  const { data: labels = [] } = useQuery({
    ...labelListOptions(wsId),
    enabled,
  });
  const query = search.trim().toLowerCase();
  const filtered = labels.filter((l) => l.name.toLowerCase().includes(query));

  return (
    <>
      <div className="px-2 py-1.5 border-b border-foreground/5">
        <input
          type="text"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t(($) => $.filters.placeholder)}
          className="w-full bg-transparent text-body placeholder:text-muted-foreground outline-none"
          autoFocus
        />
      </div>

      <div className="max-h-64 overflow-y-auto p-1">
        {filtered.map((l) => {
          const checked = selected.includes(l.id);
          const count = counts.get(l.id) ?? 0;
          return (
            <DropdownMenuCheckboxItem
              key={l.id}
              checked={checked}
              disabled={fixedIds?.has(l.id) === true}
              title={fixedIds?.has(l.id) === true ? fixedTitle : undefined}
              onCheckedChange={() => onToggle(l.id)}
              className={FILTER_ITEM_CLASS}
            >
              <HoverCheck checked={checked} />
              <LabelChip label={l} />
              {count > 0 && (
                <span className="ml-auto text-caption text-muted-foreground">
                  {count}
                </span>
              )}
            </DropdownMenuCheckboxItem>
          );
        })}

        {filtered.length === 0 && (
          <div className="px-2 py-3 text-center text-body text-muted-foreground">
            {search ? t(($) => $.filters.no_results) : t(($) => $.filters.no_labels)}
          </div>
        )}
      </div>
    </>
  );
}

/**
 * 筛选下拉里单个自定义属性的选项勾选区。
 * 单选/多选（select/multi_select）会列出属性的定义选项（圆点 + 名称）；
 * 复选框（checkbox）属性暴露"true"/"false"两个伪选项并配"是/否"文案；
 * 成员（actor）属性没有任何配置选项，候选直接来自成员目录，
 * 并把当前登录成员排在最前，让"这个属性就是我"只需一次点击。
 */

// 内联操作符单选键的键盘守卫——与下方标量输入框是同一契约：
// Escape / Tab 交给菜单处理（关闭 / 移动焦点）；
// 其余导航或选择键一律阻止冒泡，防止干扰弹出层的
// 打字搜索 / 列表导航处理器。
function stopScalarMenuKeys(event: React.KeyboardEvent) {
  if (event.key === "Escape" || event.key === "Tab") return;
  event.stopPropagation();
}
function PropertyFilterOptions({
  property,
  counts,
  selected,
  onToggle,
  onSetValues,
  fixedIds,
  fixedTitle,
}: {
  property: IssueProperty;
  counts: Map<string, number> | undefined;
  selected: PropertyFilterValue[];
  onToggle: (optionId: string) => void;
  /** 整体替换该属性的筛选值集合（仅标量类型使用）。 */
  onSetValues: (optionIds: PropertyFilterValue[]) => void;
  fixedIds?: Set<string>;
  fixedTitle?: string;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const currentUserId = useAuthStore((s) => s.user?.id);
  const actorProperty = isActorPropertyType(property.type);
  // 标量属性（文本 / 数字 / 日期 / 网址）没有选项列表——筛选菜单里给它
  // 一个值输入框外加"无值"选项。
  const { data: actorMembers = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: actorProperty,
  });

  const actorOptions = useMemo(() => {
    if (!actorProperty) return [];
    return actorMembers
      .slice()
      .sort((a, b) => {
        if (a.user_id === currentUserId) return -1;
        if (b.user_id === currentUserId) return 1;
        return 0;
      })
      .map((m) => ({
        id: formatActorRef("member", m.user_id),
        name: m.name,
        actorType: "member" as const,
        actorId: m.user_id,
      }));
  }, [actorProperty, actorMembers, currentUserId]);

  // "无值"对所有属性类型都提供：它选出"该属性未设置"的问题，
  // 与勾选下方某个具体选项正好相反。
  const noValueOption = {
    id: NO_PROPERTY_VALUE,
    name: t(($) => $.pickers.custom_property.none),
    color: undefined as string | undefined,
    actorType: undefined as string | undefined,
    actorId: undefined as string | undefined,
  };
  // 标量值状态放在顶层，让 hooks 保持无条件调用（Rules of Hooks）：
  // 它只对文本/数字/日期/网址渲染，但无论走哪个分支都必须声明。
  // 草稿会跟随已提交的标量成员同步，因此别处清空或重写筛选后，
  // 不会用过期输入把旧值写回去。
  const committedMember = selected.find((member) => member !== NO_PROPERTY_VALUE);
  const committedScalar =
    typeof committedMember === "object" ? committedMember.value : (committedMember ?? "");
  const committedOp: PropertyFilterOp | "is" =
    typeof committedMember === "object" ? committedMember.op : "is";
  const hasNoValue = selected.includes(NO_PROPERTY_VALUE);
  const [draft, setDraft] = useState(committedScalar);
  useEffect(() => setDraft(committedScalar), [committedScalar]);
  // 在打开的菜单里选好但尚未提交的操作符；null 表示沿用已提交成员
  //（没有时即"相等"）。已提交成员一旦变化就重置——包括本菜单刚提交之后——
  // 避免别处重写的值继承到过期操作符。
  const [pendingOp, setPendingOp] = useState<PropertyFilterOp | "is" | null>(null);
  const committedKey =
    committedMember === undefined
      ? ""
      : typeof committedMember === "object"
        ? `op:${committedMember.op}:${committedMember.value}`
        : `eq:${committedMember}`;
  useEffect(() => setPendingOp(null), [committedKey]);
  const options = [
    ...(actorProperty
      ? actorOptions.map((option) => ({
          id: option.id,
          name: option.name,
          color: undefined as string | undefined,
          actorType: option.actorType as string | undefined,
          actorId: option.actorId as string | undefined,
        }))
      : property.type === "checkbox"
        ? [
            { id: "true", name: t(($) => $.pickers.custom_property.true_label), color: undefined, actorType: undefined, actorId: undefined },
            { id: "false", name: t(($) => $.pickers.custom_property.false_label), color: undefined, actorType: undefined, actorId: undefined },
          ]
        : (property.config.options ?? []).map((option) => ({
            id: option.id,
            name: option.name,
            color: option.color as string | undefined,
            actorType: undefined as string | undefined,
            actorId: undefined as string | undefined,
          }))),
    noValueOption,
  ];

  if (isScalarPropertyType(property.type)) {
    const placeholder =
      property.type === "url"
        ? t(($) => $.pickers.custom_property.url_placeholder)
        : property.type === "number"
          ? t(($) => $.pickers.custom_property.number_placeholder)
          : t(($) => $.pickers.custom_property.value_placeholder);
    const noneCount = counts?.get(NO_PROPERTY_VALUE) ?? 0;
    // 已保存视图会锁定这个维度：值（连同操作符）与"无值"都属于视图身份的一部分，
    // 因此两者都不能就地修改。
    const locked = fixedIds !== undefined && fixedIds.size > 0;
    // "is" 表示相等，提交的是裸字符串（即操作符出现之前的形态）；
    // 其余操作符提交操作符对象。服务端与匹配器对未知形态都做保守处理。
    const effectiveOp: PropertyFilterOp | "is" = pendingOp ?? committedOp;
    const scalarOperatorLabel = (op: PropertyFilterOp): string => {
      if (op === "contains") return t(($) => $.pickers.custom_property.op_contains);
      if (op === "before") return t(($) => $.pickers.custom_property.op_before);
      if (op === "after") return t(($) => $.pickers.custom_property.op_after);
      return PROPERTY_FILTER_OP_SYMBOLS[op] ?? op;
    };
    const opButtons: { op: PropertyFilterOp | "is"; label: string }[] = [
      {
        op: "is",
        label:
          property.type === "number"
            ? "="
            : t(($) => $.pickers.custom_property.op_is),
      },
      ...(PROPERTY_FILTER_OPS_BY_TYPE[property.type] ?? []).map((op) => ({
        op,
        label: scalarOperatorLabel(op),
      })),
    ];
    const commitValue = (raw: string, op: PropertyFilterOp | "is" = effectiveOp) => {
      const value = raw.trim();
      // NO_PROPERTY_VALUE 是保留的"无值"哨兵——不能把它当字面值来筛选。
      // 值与"无值"像其他属性类型那样组合：提交一个值只替换"值"这一份成员，
      // 并保留"无值"的勾选状态。
      if (value === NO_PROPERTY_VALUE) return;
      const member: PropertyFilterValue | undefined = value
        ? op === "is"
          ? value
          : { op, value }
        : undefined;
      onSetValues([
        ...(member ? [member] : []),
        ...(hasNoValue ? [NO_PROPERTY_VALUE] : []),
      ]);
    };
    const applyOp = (op: PropertyFilterOp | "is") => {
      // 显式点击会用所选操作符立即提交当前草稿，而输入框要等 Enter/失焦才提交。
      // 草稿为空时什么都不提交，此时选择挂在 pendingOp 上、由下一次
      // Enter/失焦的提交带走——如果在这里清掉 pendingOp，
      // 会悄悄把那一次提交降级回"相等"。
      setPendingOp(op);
      commitValue(draft, op);
    };
    return (
      <>
        {opButtons.length > 1 && (
          <div
            role="radiogroup"
            aria-label={t(($) => $.pickers.custom_property.operator_label)}
            className="flex flex-wrap gap-1 px-2 pt-1.5"
          >
            {opButtons.map(({ op, label }) => {
              const active = effectiveOp === op;
              return (
                <label
                  key={op}
                  className={locked ? "cursor-not-allowed" : "cursor-pointer"}
                >
                  <input
                    type="radio"
                    name={`property-filter-op-${property.id}`}
                    value={op}
                    checked={active}
                    disabled={locked}
                    onChange={() => applyOp(op)}
                    onKeyDown={stopScalarMenuKeys}
                    className="peer sr-only"
                  />
                  <span
                    className={`inline-flex h-6 items-center rounded-md px-1.5 text-caption transition-colors peer-focus-visible:outline-none peer-focus-visible:ring-2 peer-focus-visible:ring-ring ${
                      active
                        ? "bg-accent font-medium text-foreground"
                        : "text-muted-foreground hover:bg-accent/50 hover:text-foreground"
                    }`}
                  >
                    {label}
                  </span>
                </label>
              );
            })}
          </div>
        )}
        <div className="px-2 py-1.5">
          <Input
            type={property.type === "number" ? "number" : property.type === "date" ? "date" : "text"}
            step={property.type === "number" ? "any" : undefined}
            inputMode={property.type === "number" ? "decimal" : undefined}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onBlur={(event) => {
              // 点击"无值"菜单项会把焦点移出输入框、先触发 blur。这次过早的提交
              // 会在其点击处理器读到勾选状态之前就把受控状态翻转了——
              // 所以当焦点是移进本弹层内的菜单项时，跳过 blur 提交，
              // 让 onCheckedChange 依据 blur 之前的状态做决定。
              const next = event.relatedTarget;
              const movedToMenuItem =
                next instanceof HTMLElement &&
                next.closest('[role="menuitemcheckbox"]') !== null;
              if (!movedToMenuItem) commitValue(draft);
            }}
            onKeyDown={(event) => {
              // Base UI 的菜单弹层把打字搜索 + 列表导航处理器都挂在 role="menu"
              // 元素上并对每个可打印键 stopEvent()——如果不阻止传播，输入框将
              // 一个字符都收不到（数字/日期输入框的上下键也会被劫持）。
              // Escape/Tab 留给菜单去关闭/移动焦点；Enter 提交且仍需
              // stopPropagation，避免冒泡后触发被高亮的"无值"项。
              if (event.key === "Escape" || event.key === "Tab") return;
              if (event.key === "Enter") commitValue(draft);
              event.stopPropagation();
            }}
            disabled={locked}
            placeholder={placeholder}
            className="h-8"
          />
        </div>
        <DropdownMenuCheckboxItem
          checked={hasNoValue}
          disabled={locked}
          onCheckedChange={(checked) => {
            // "无值"与"值"在同一个 OR 集合里切换成员——勾选/取消都从不改动
            // 已提交的"值"成员，因此取消勾选能原样恢复它，无需草稿往返。
            const valueMembers = selected.filter((id) => id !== NO_PROPERTY_VALUE);
            onSetValues(
              checked
                ? [...valueMembers, NO_PROPERTY_VALUE]
                : valueMembers,
            );
          }}
          className={FILTER_ITEM_CLASS}
        >
          <HoverCheck checked={hasNoValue} />
          <span className="truncate">{noValueOption.name}</span>
          {noneCount > 0 && (
            <span className="ml-auto text-caption text-muted-foreground">{noneCount}</span>
          )}
        </DropdownMenuCheckboxItem>
      </>
    );
  }

  return (
    <>
      {options.map((option) => {
        const checked = selected.includes(option.id);
        const count = counts?.get(option.id) ?? 0;
        return (
          <DropdownMenuCheckboxItem
            key={option.id}
            checked={checked}
            disabled={fixedIds?.has(option.id) === true}
            title={fixedIds?.has(option.id) === true ? fixedTitle : undefined}
            onCheckedChange={() => onToggle(option.id)}
            className={FILTER_ITEM_CLASS}
          >
            <HoverCheck checked={checked} />
            {option.actorType && option.actorId && (
              <ActorAvatar actorType={option.actorType} actorId={option.actorId} size="sm" />
            )}
            {option.color && (
              <span
                className="size-2.5 shrink-0 rounded-full"
                style={{ backgroundColor: option.color }}
              />
            )}
            <span className="truncate">{option.name}</span>
            {count > 0 && (
              <span className="ml-auto text-caption text-muted-foreground">
                {count}
              </span>
            )}
          </DropdownMenuCheckboxItem>
        );
      })}
    </>
  );
}

// ---------------------------------------------------------------------------
// 日期子菜单内容
// ---------------------------------------------------------------------------

function DateSubContent({
  value,
  onChange,
}: {
  value: IssueDateFilter | null;
  onChange: (filter: IssueDateFilter | null) => void;
}) {
  const { t } = useT("issues");
  const [field, setField] = useState<IssueDateField>(value?.field ?? "created_at");
  // 做成受控组件：点"应用"可以单独收起日历，而筛选菜单本身保持展开
  //（菜单的全局规则：筛选编辑从不关闭菜单）。
  const [calendarOpen, setCalendarOpen] = useState(false);
  const [range, setRange] = useState<LocalDateRange | undefined>(() => {
    if (!value) return undefined;
    const from = dateOnlyToLocalDate(value.from);
    if (!from) return undefined;
    return { from, to: dateOnlyToLocalDate(value.to) };
  });

  const setFieldValue = (next: IssueDateField) => {
    setField(next);
    if (value) onChange({ ...value, field: next });
  };

  const applyPreset = (days: 1 | 3 | 7) => {
    onChange({
      field,
      from: addDaysDateOnly(1 - days),
      to: todayDateOnly(),
    });
  };

  const applyCustom = () => {
    if (!range?.from) return;
    const [from, to] = normalizeDateRange(range.from, range.to ?? range.from);
    onChange({
      field,
      from: toDateOnly(from),
      to: toDateOnly(to),
    });
    setCalendarOpen(false);
  };

  return (
    <>
      <DropdownMenuGroup>
        <DropdownMenuLabel>{t(($) => $.filters.date_field)}</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={field} onValueChange={(next) => setFieldValue(next as IssueDateField)}>
          {(["created_at", "updated_at"] as const).map((option) => (
            // 选择日期字段只是下方快捷项的一个参数，并非最终动作——
            // 保持菜单展开，让用户能继续选快捷项或自定义区间。
            <DropdownMenuRadioItem key={option} value={option} closeOnClick={false}>
              {t(($) => $.filters[DATE_FIELD_LABEL_KEY[option]])}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuGroup>

      <DropdownMenuSeparator />
      {/* 快捷项会直接设置筛选值，语义等同勾选状态选项——保持菜单展开，
          以便继续做多维度细筛。只有"重置"（终结动作）会关闭菜单。 */}
      <DropdownMenuItem closeOnClick={false} onClick={() => applyPreset(1)}>
        {t(($) => $.filters.date_today)}
      </DropdownMenuItem>
      <DropdownMenuItem closeOnClick={false} onClick={() => applyPreset(3)}>
        {t(($) => $.filters.date_last_3_days)}
      </DropdownMenuItem>
      <DropdownMenuItem closeOnClick={false} onClick={() => applyPreset(7)}>
        {t(($) => $.filters.date_last_7_days)}
      </DropdownMenuItem>

      <div className="px-1.5 py-1">
        <Popover open={calendarOpen} onOpenChange={setCalendarOpen}>
          <PopoverTrigger
            render={
              <Button
                variant="ghost"
                size="sm"
                className="h-7 w-full justify-start px-0 text-body font-normal"
              >
                {t(($) => $.filters.date_custom_range)}
              </Button>
            }
          />
          <PopoverContent align="start" side="right" className="w-auto gap-0 p-0">
            <Calendar
              mode="range"
              selected={range}
              onSelect={(next) => setRange(next)}
              captionLayout="dropdown"
            />
            <div className="flex justify-end border-t p-2">
              <Button size="sm" onClick={applyCustom} disabled={!range?.from}>
                {t(($) => $.filters.date_apply)}
              </Button>
            </div>
          </PopoverContent>
        </Popover>
      </div>

      {value && (
        <>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            closeOnClick={false}
            onClick={() => {
              setRange(undefined);
              onChange(null);
            }}
          >
            {t(($) => $.filters.date_clear)}
          </DropdownMenuItem>
        </>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// ViewRefreshIndicator（视图刷新指示器）
// ---------------------------------------------------------------------------

/**
 * 头部控件区中一个中性的"视图正在重新校验"占位指示灯。
 * 由宿主页面的 isRefreshing 驱动（占位式重新校验：排序/日期变化、看板分组
 * 切换等都会触发）——刻意与具体触发动作解耦，任何让请求在途的视图变更都会点亮它。
 *
 * 该槽位宽度固定，出现/消失都不会挤动旁边控件；300ms 的延迟出现让快速网络下
 * 保持无指示（亚秒响应无需反馈），慢网络下用户能收到"正在处理"的信号，
 * 而不是一次毫无反馈的点击。
 */
export function ViewRefreshIndicator({ active }: { active: boolean }) {
  return (
    <span className="flex w-4 shrink-0 items-center justify-center">
      {active && (
        <span className="animate-in fade-in fill-mode-backwards [animation-delay:300ms]">
          <Spinner className="size-3.5 text-muted-foreground" />
        </span>
      )}
    </span>
  );
}

// ---------------------------------------------------------------------------
// IssuesHeader（问题页头部）
// ---------------------------------------------------------------------------

/**
 * 问题列表页的头部工具条。关键 props：
 * - scopedIssues：当前范围过滤后、可用于展示与计数的问题列表；
 * - workingAgents：surface 范围内的"代理工作中"概要（驱动 agents-working 角标）；
 * - allowGantt：是否开放甘特视图入口（目前仅项目详情页为 true）；
 * - dateFilter / onDateFilterChange：日期筛选状态及其回调——日期筛选独立于
 *   视图 store，由宿主负责持有，这里只透传给筛选菜单与 chips 条；
 * - isRefreshing：为 true 时在控件区右侧点亮"正在刷新"指示；
 * - facetCountsExact / tableFacetCounts / onTableFacetChange：表格模式
 *   分组菜单需要的后端 facet 计数与"按状态分组"选中态回传；
 * - saveViewScope：决定"另存为视图"的归档范围（工作区/项目/我的）；
 *   传 null 时整条保存入口不展示。
 */
export function IssuesHeader({
  scopedIssues,
  workingAgents,
  allowGantt = false,
  dateFilter = null,
  onDateFilterChange,
  isRefreshing = false,
  facetCountsExact = true,
  tableFacetCounts,
  onTableFacetChange,
  saveViewScope = { kind: "workspace" },
}: {
  scopedIssues: Issue[];
  /** 参见 IssueSurfaceController.workingAgents——"代理工作中"角标背后、
   *  按当前 surface 范围投影出的代理列表。 */
  workingAgents: WorkingAgentSummary[] | undefined;
  allowGantt?: boolean;
  dateFilter?: IssueDateFilter | null;
  onDateFilterChange?: (filter: IssueDateFilter | null) => void;
  isRefreshing?: boolean;
  /** 说明见 IssueDisplayControls.facetCountsExact。 */
  facetCountsExact?: boolean;
  tableFacetCounts?: IssueTableFacetsResponse;
  onTableFacetChange?: (facet: IssueTableFacetSpec | null) => void;
  /** "另存为视图"把视图归档到哪个范围。/issues 用工作区默认；
   *  项目详情页传入它自己的项目范围；传 `null` 则完全不展示保存入口。 */
  saveViewScope?: SaveViewScope | null;
}) {
  const { t } = useT("issues");
  const [saveViewOpen, setSaveViewOpen] = useState(false);
  const headerWsId = useWorkspaceId();
  const viewListScope: IssueViewScope | null = saveViewScope
    ? saveViewScope.kind === "project"
      ? { scope_type: "project", scope_id: saveViewScope.projectId }
      : { scope_type: saveViewScope.kind }
    : null;
  const { activeView, views, viewsReady, setActive, missing } = useActiveIssueView(
    headerWsId,
    viewListScope,
  );
  // 当前打开的视图不见了（被删除 / 访问权限被收回）：回退到内置默认视图
  // 并 toast 告知用户——绝不让页面变成空白（§7）。
  useEffect(() => {
    if (missing) {
      setActive(null);
      toast.info(t(($) => $.view_selector.unavailable));
    }
  }, [missing, setActive, t]);
  const viewBaseline = useMemo(
    () => (activeView ? baselineFromQuery(activeView.query) : undefined),
    [activeView],
  );
  const [editTarget, setEditTarget] = useState<{
    view: IssueView;
    fromDefinition: boolean;
  } | null>(null);
  const currentUserId = useAuthStore((s) => s.user?.id ?? null);
  const isViewOwner = !!activeView && activeView.owner_id === currentUserId;
  // 页签归属于"当前这个页面"（全局 Issues 与各项目详情彼此独立）——
  // 见 IssuesScopePageKey。头部既渲染这些页签，也用它们预填
  // 保存视图对话框的默认变体。
  const scopePage: IssuesScopePageKey =
    saveViewScope?.kind === "project"
      ? `project:${saveViewScope.projectId}`
      : "issues";
  const scope = useIssuesScope(scopePage);
  const setScopeForPage = useIssuesScopeStore((s) => s.setScope);
  const setScope = useCallback(
    (next: IssuesScope) => setScopeForPage(scopePage, next),
    [setScopeForPage, scopePage],
  );
  // 保存对话框的默认变体：已打开保存视图时以视图自身变体为准（屏幕上的行
  // 就是该变体——副本不能悄悄扩大到页面页签范围）；否则沿用页面页签。
  // 按基本类型记忆化：对话框在这个 prop 的引用变化时会重置草稿，
  // 若头部每次渲染都产生新对象，任何后台重取都会清掉用户敲了一半的
  // 名称。
  const dialogActorKind = activeView
    ? actorKindForViewVariant(activeView.scope_variant)
    : scope;
  const dialogProjectId =
    saveViewScope?.kind === "project" ? saveViewScope.projectId : null;
  const dialogMyVariant =
    saveViewScope?.kind === "my" ? saveViewScope.variant : null;
  const dialogScope = useMemo<SaveViewScope | null>(() => {
    if (dialogMyVariant) return { kind: "my", variant: dialogMyVariant };
    if (dialogProjectId)
      return {
        kind: "project",
        projectId: dialogProjectId,
        actorKind: dialogActorKind,
      };
    if (saveViewScope) return { kind: "workspace", actorKind: dialogActorKind };
    return null;
    // eslint-disable-next-line react-hooks/exhaustive-deps -- identity keyed on the primitive projections
  }, [dialogMyVariant, dialogProjectId, dialogActorKind, !!saveViewScope]);
  // 把工作区的"代理工作中"角标绑定到活动视图 store，让共用 IssuesHeader
  // 的宿主（/issues 与项目详情页）与其他显示控件切换同一份筛选状态。
  // /my-issues 有自己的独立兄弟头部，通过显式传参控制该角标。
  const agentRunningFilter = useViewStore((s) => s.agentRunningFilter);
  const toggleAgentRunningFilter = useViewStore(
    (s) => s.toggleAgentRunningFilter,
  );
  const SCOPE_LABEL_KEY: Record<IssuesScope, "all_label" | "members_label" | "agents_label"> = {
    all: "all_label",
    members: "members_label",
    agents: "agents_label",
  };
  const SCOPE_DESC_KEY: Record<IssuesScope, "all_description" | "members_description" | "agents_description"> = {
    all: "all_description",
    members: "members_description",
    agents: "agents_description",
  };

  const scopeLabel = t(($) => $.scope[SCOPE_LABEL_KEY[scope]]);

  return (
    <>
    <div className={cn("min-h-12 shrink-0 py-2 [-webkit-overflow-scrolling:touch]", PAGE_GUTTER)}>
      <div className="flex w-full min-w-0 items-start justify-between gap-2">
        {/* 左侧：视图栏——内置页签与已保存视图拼成一行扁平、按用户排序的
            横向列表；放不下时换行而非溢出。 */}
        <div className="hidden min-w-0 flex-1 md:block">
          {saveViewScope && viewListScope && (
            <ViewBar
              wsId={headerWsId}
              scope={viewListScope}
              builtins={SCOPE_VALUES.map((s) => ({
                key: s,
                label: t(($) => $.scope[SCOPE_LABEL_KEY[s]]),
                description: t(($) => $.scope[SCOPE_DESC_KEY[s]]),
                active: !activeView && scope === s,
                onSelect: () => {
                  // 点击内置页签会退出任何已打开的保存视图，surface
                  // 回到该页签未改动过的原始状态。
                  if (activeView) setActive(null);
                  setScope(s);
                },
              }))}
              views={views}
              viewsReady={viewsReady}
              activeView={activeView}
              onSelectView={(view) => setActive(view ? view.id : null)}
              onNewView={() => {
                setEditTarget(null);
                setSaveViewOpen(true);
              }}
              onEditView={(view) => {
                setEditTarget({ view, fromDefinition: true });
                setSaveViewOpen(true);
              }}
            />
          )}
        </div>

        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="outline"
                size="sm"
                className="shrink-0 gap-1 text-muted-foreground md:hidden"
              >
                <span className="truncate">{scopeLabel}</span>
                <ChevronDown className="size-3 text-muted-foreground" />
              </Button>
            }
          />
          <DropdownMenuContent align="start" className="w-auto">
            <DropdownMenuRadioGroup value={scope} onValueChange={(value) => setScope(value as IssuesScope)}>
              {SCOPE_VALUES.map((s) => (
                <DropdownMenuRadioItem key={s} value={s}>
                  {t(($) => $.scope[SCOPE_LABEL_KEY[s]])}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>

        <div className="flex shrink-0 items-center gap-1">
          {agentRunningFilter && (
            <span className="mr-1 hidden text-caption text-muted-foreground md:inline">
              {t(($) => $.agent_activity.filter_active_label)}
            </span>
          )}
          <WorkspaceAgentWorkingChip
            value={agentRunningFilter}
            onToggle={toggleAgentRunningFilter}
            agents={workingAgents}
          />
          <IssueDisplayControls
            scopedIssues={scopedIssues}
            allowGantt={allowGantt}
            dateFilter={dateFilter}
            onDateFilterChange={onDateFilterChange}
            facetCountsExact={facetCountsExact}
            tableFacetCounts={tableFacetCounts}
            onTableFacetChange={onTableFacetChange}
            viewBaseline={viewBaseline}
          />
          <ViewRefreshIndicator active={isRefreshing} />
        </div>
      </div>
    </div>
    <FilterChipsBar
      dateFilter={dateFilter}
      onDateFilterChange={onDateFilterChange}
      viewBaseline={viewBaseline}
      saveLabel={
        activeView
          ? isViewOwner
            ? t(($) => $.filters.chip_edit)
            : t(($) => $.filters.chip_save_as)
          : undefined
      }
      onSave={
        saveViewScope
          ? () => {
              setEditTarget(
                activeView && isViewOwner
                  ? { view: activeView, fromDefinition: false }
                  : null,
              );
              setSaveViewOpen(true);
            }
          : undefined
      }
    />
    {dialogScope && (
      <SaveViewDialog
        open={saveViewOpen}
        onOpenChange={setSaveViewOpen}
        scope={dialogScope}
        editView={editTarget?.view ?? null}
        seedFromDefinition={editTarget?.fromDefinition ?? false}
      />
    )}
    </>
  );
}

/**
 * 完整的筛选菜单（覆盖全部维度、搜索与计数），挂在调用方提供的触发器元素上。
 * 工具栏把它包进带文字按钮；保存视图对话框则把它接到一个虚拟的
 * "添加筛选"chip 上、目标是草稿 store。计数跟随 `scopedIssues`——
 * 不传就不显示数字。
 */
export function IssueFilterMenu({
  trigger,
  tooltip,
  scopedIssues = NO_COUNT_ISSUES,
  facetCountsExact = true,
  tableFacetCounts,
  onTableFacetChange,
  dateFilter = null,
  onDateFilterChange,
  onOpenChange,
  freezeAnchor = false,
  viewBaseline,
}: {
  trigger: React.ReactElement;
  tooltip?: string;
  scopedIssues?: Issue[];
  facetCountsExact?: boolean;
  tableFacetCounts?: IssueTableFacetsResponse;
  onTableFacetChange?: (facet: IssueTableFacetSpec | null) => void;
  dateFilter?: IssueDateFilter | null;
  onDateFilterChange?: (filter: IssueDateFilter | null) => void;
  /** 与菜单开合状态保持同步。 */
  onOpenChange?: (open: boolean) => void;
  /** 把展开的菜单钉在"打开那一刻"触发器所在的位置（虚拟锚点）。
   *  开启后，chips 增删或换行可以让触发器自由移动，
   *  而打开的菜单绝不会在指针下方漂移。 */
  freezeAnchor?: boolean;
  /** 已打开的保存视图：视图已固定的值渲染为勾选并禁用
   *  （表示"已包含在该视图里"）；其余选项照常可选。 */
  viewBaseline?: IssueViewBaseline;
}) {
  const { t } = useT("issues");
  const triggerRef = useRef<HTMLElement>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [frozenAnchor, setFrozenAnchor] = useState<{ getBoundingClientRect: () => DOMRect } | null>(null);
  const statusFilters = useViewStore((s) => s.statusFilters);
  const priorityFilters = useViewStore((s) => s.priorityFilters);
  const assigneeFilters = useViewStore((s) => s.assigneeFilters);
  const includeNoAssignee = useViewStore((s) => s.includeNoAssignee);
  const creatorFilters = useViewStore((s) => s.creatorFilters);
  const projectFilters = useViewStore((s) => s.projectFilters);
  const includeNoProject = useViewStore((s) => s.includeNoProject);
  const labelFilters = useViewStore((s) => s.labelFilters);
  const propertyFilters = useViewStore((s) => s.propertyFilters);
  const viewStoreApi = useViewStoreApi();
  const act = viewStoreApi.getState();
  const wsId = useWorkspaceId();
  const statusOptions = useStatusOptions(wsId);
  const hasPropertyFilters = Object.values(propertyFilters).some(
    (selected) => selected.length > 0,
  );
  const { data: workspaceProperties = [] } = useQuery({
    ...propertyListOptions(wsId),
    enabled: menuOpen || hasPropertyFilters,
  });
  const filterableProperties = useMemo(
    () =>
      workspaceProperties.filter((p) => isFilterablePropertyType(p.type)),
    [workspaceProperties],
  );
  const counts = useIssueCounts(
    facetCountsExact ? scopedIssues : NO_COUNT_ISSUES,
    tableFacetCounts,
  );
  const showDateFilter = !!onDateFilterChange;
  const effectivePropertyFilters = useMemo(() => {
    const activeIds = new Set(filterableProperties.map((p) => p.id));
    return Object.fromEntries(
      Object.entries(propertyFilters).filter(([id, selected]) => selected.length > 0 && activeIds.has(id)),
    );
  }, [filterableProperties, propertyFilters]);
  const hasActiveFilters =
    getActiveFilterCount(
      {
        statusFilters,
        priorityFilters,
        propertyFilters: effectivePropertyFilters,
        assigneeFilters,
        includeNoAssignee,
        creatorFilters,
        projectFilters,
        includeNoProject,
        labelFilters,
        dateFilter: showDateFilter ? dateFilter : null,
      },
      viewBaseline,
    ) > 0;
  const fixedTitle = viewBaseline ? t(($) => $.filters.in_view) : undefined;
  const dateFilterLabel = showDateFilter && dateFilter
    ? `${t(($) => $.filters[DATE_FIELD_LABEL_KEY[dateFilter.field]])}: ${
        dateFilter.from === dateFilter.to
          ? shortDateLabel(dateFilter.from)
          : `${shortDateLabel(dateFilter.from)} - ${shortDateLabel(dateFilter.to)}`
      }`
    : null;

  // Base UI 的 render-prop 会把它自己的 ref 与我们给的 ref 合并，
  // 所以克隆调用方元素只会额外带上采集矩形所需的 ref。
  const anchoredTrigger = freezeAnchor
    ? cloneElement(trigger, { ref: triggerRef } as Partial<React.HTMLAttributes<HTMLElement>>)
    : trigger;

  return (
    <DropdownMenu
      onOpenChange={(open) => {
        setMenuOpen(open);
        if (freezeAnchor) {
          if (open && triggerRef.current) {
            const rect = triggerRef.current.getBoundingClientRect();
            setFrozenAnchor({ getBoundingClientRect: () => rect });
          } else if (!open) {
            setFrozenAnchor(null);
          }
        }
        if (!open) onTableFacetChange?.(null);
        onOpenChange?.(open);
      }}
    >
      {tooltip ? (
        <Tooltip>
          <DropdownMenuTrigger render={<TooltipTrigger render={anchoredTrigger} />} />
          <TooltipContent side="bottom">{tooltip}</TooltipContent>
        </Tooltip>
      ) : (
        <DropdownMenuTrigger render={anchoredTrigger} />
      )}
          <DropdownMenuContent align="end" className="w-auto" anchor={frozenAnchor ?? undefined}>
            {/* 状态 */}
            <DropdownMenuSub
              onOpenChange={(open) =>
                onTableFacetChange?.(open ? { kind: "status" } : null)
              }
            >
              <DropdownMenuSubTrigger>
                <CircleDot className="size-3.5" />
                <span className="flex-1">{t(($) => $.filters.section_status)}</span>
                {statusFilters.length > 0 && (
                  <span className="text-caption text-primary font-medium">
                    {statusFilters.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-auto min-w-48">
                {/* 选项来自工作区目录，因此自定义状态也可参与筛选——否则一个被移
                    到自定义状态上的问题就筛不出来了。按类别顺序排成一条平铺列表：
                    状态图标已表达类别，若再按类别加小标题只会徒增菜单高度，
                    没有新增信息（MUL-6243, MUL-6399）。 */}
                {statusOptions.map((option) => {
                  const checked = statusFilters.includes(option.key);
                  const count = counts.status.get(option.key) ?? 0;
                  const fixed = viewBaseline?.status.has(option.key) === true;
                  return (
                    <DropdownMenuCheckboxItem
                      key={option.key}
                      checked={checked}
                      disabled={fixed}
                      title={fixed ? fixedTitle : undefined}
                      onCheckedChange={() => act.toggleStatusFilter(option.key)}
                      className={FILTER_ITEM_CLASS}
                    >
                      <HoverCheck checked={checked} />
                      <StatusIcon
                        status={option.key}
                        category={option.category}
                        color={option.color}
                        className="h-3.5 w-3.5"
                      />
                      {option.label}
                      {count > 0 && (
                        <span className="ml-auto text-caption text-muted-foreground">
                          {t(($) => $.filters.issue_count, { count })}
                        </span>
                      )}
                    </DropdownMenuCheckboxItem>
                  );
                })}
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {/* 优先级 */}
            <DropdownMenuSub
              onOpenChange={(open) =>
                onTableFacetChange?.(open ? { kind: "priority" } : null)
              }
            >
              <DropdownMenuSubTrigger>
                <SignalHigh className="size-3.5" />
                <span className="flex-1">{t(($) => $.filters.section_priority)}</span>
                {priorityFilters.length > 0 && (
                  <span className="text-caption text-primary font-medium">
                    {priorityFilters.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-auto min-w-44">
                {PRIORITY_DISPLAY_ORDER.map((p) => {
                  const checked = priorityFilters.includes(p);
                  const count = counts.priority.get(p) ?? 0;
                  const fixed = viewBaseline?.priority.has(p) === true;
                  return (
                    <DropdownMenuCheckboxItem
                      key={p}
                      checked={checked}
                      disabled={fixed}
                      title={fixed ? fixedTitle : undefined}
                      onCheckedChange={() => act.togglePriorityFilter(p)}
                      className={FILTER_ITEM_CLASS}
                    >
                      <HoverCheck checked={checked} />
                      <PriorityIcon priority={p} />
                      {t(($) => $.priority[p])}
                      {count > 0 && (
                        <span className="ml-auto text-caption text-muted-foreground">
                          {t(($) => $.filters.issue_count, { count })}
                        </span>
                      )}
                    </DropdownMenuCheckboxItem>
                  );
                })}
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {showDateFilter && onDateFilterChange && (
              <DropdownMenuSub>
                <DropdownMenuSubTrigger>
                  <CalendarDays className="size-3.5" />
                  <span className="flex-1">{t(($) => $.filters.section_date)}</span>
                  {dateFilterLabel && (
                    <span className="max-w-36 truncate text-caption text-primary font-medium">
                      {dateFilterLabel}
                    </span>
                  )}
                </DropdownMenuSubTrigger>
                <DropdownMenuSubContent className="w-56">
                  <DateSubContent
                    value={dateFilter}
                    onChange={onDateFilterChange}
                  />
                </DropdownMenuSubContent>
              </DropdownMenuSub>
            )}

            {/* 负责人 */}
            <DropdownMenuSub
              onOpenChange={(open) =>
                onTableFacetChange?.(open ? { kind: "assignee" } : null)
              }
            >
              <DropdownMenuSubTrigger>
                <User className="size-3.5" />
                <span className="flex-1">{t(($) => $.filters.section_assignee)}</span>
                {(assigneeFilters.length > 0 || includeNoAssignee) && (
                  <span className="text-caption text-primary font-medium">
                    {assigneeFilters.length + (includeNoAssignee ? 1 : 0)}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-auto min-w-52 p-0">
                <ActorSubContent
                  enabled={menuOpen}
                  counts={counts.assignee}
                  selected={assigneeFilters}
                  onToggle={act.toggleAssigneeFilter}
                  showNoAssignee
                  includeNoAssignee={includeNoAssignee}
                  onToggleNoAssignee={act.toggleNoAssignee}
                  noAssigneeCount={counts.noAssignee}
                  fixedKeys={viewBaseline?.assignee}
                  noAssigneeFixed={viewBaseline?.includeNoAssignee === true}
                  fixedTitle={fixedTitle}
                />
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {/* 创建人 */}
            <DropdownMenuSub
              onOpenChange={(open) =>
                onTableFacetChange?.(open ? { kind: "creator" } : null)
              }
            >
              <DropdownMenuSubTrigger>
                <UserPen className="size-3.5" />
                <span className="flex-1">{t(($) => $.filters.section_creator)}</span>
                {creatorFilters.length > 0 && (
                  <span className="text-caption text-primary font-medium">
                    {creatorFilters.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-auto min-w-52 p-0">
                <ActorSubContent
                  enabled={menuOpen}
                  counts={counts.creator}
                  selected={creatorFilters}
                  onToggle={act.toggleCreatorFilter}
                  showSquads={false}
                  fixedKeys={viewBaseline?.creator}
                  fixedTitle={fixedTitle}
                />
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {/* 项目 */}
            <DropdownMenuSub
              onOpenChange={(open) =>
                onTableFacetChange?.(open ? { kind: "project" } : null)
              }
            >
              <DropdownMenuSubTrigger>
                <FolderKanban className="size-3.5" />
                <span className="flex-1">{t(($) => $.filters.section_project)}</span>
                {(projectFilters.length > 0 || includeNoProject) && (
                  <span className="text-caption text-primary font-medium">
                    {projectFilters.length + (includeNoProject ? 1 : 0)}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-auto min-w-52 p-0">
                <ProjectSubContent
                  enabled={menuOpen}
                  counts={counts.project}
                  selected={projectFilters}
                  onToggle={act.toggleProjectFilter}
                  includeNoProject={includeNoProject}
                  onToggleNoProject={act.toggleNoProject}
                  noProjectCount={counts.noProject}
                  fixedIds={viewBaseline?.project}
                  noProjectFixed={viewBaseline?.includeNoProject === true}
                  fixedTitle={fixedTitle}
                />
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {/* 标签 */}
            <DropdownMenuSub
              onOpenChange={(open) =>
                onTableFacetChange?.(open ? { kind: "label" } : null)
              }
            >
              <DropdownMenuSubTrigger>
                <Tag className="size-3.5" />
                <span className="flex-1">{t(($) => $.filters.section_label)}</span>
                {labelFilters.length > 0 && (
                  <span className="text-caption text-primary font-medium">
                    {labelFilters.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-auto min-w-52 p-0">
                <LabelSubContent
                  enabled={menuOpen}
                  counts={counts.label}
                  selected={labelFilters}
                  onToggle={act.toggleLabelFilter}
                  fixedIds={viewBaseline?.label}
                  fixedTitle={fixedTitle}
                />
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            {/* 自定义属性——每个可筛选定义对应一个子菜单 */}
            {filterableProperties.length > 0 && <DropdownMenuSeparator />}
            {filterableProperties.map((property) => {
              const selected = propertyFilters[property.id] ?? [];
              return (
                <DropdownMenuSub
                  key={property.id}
                  onOpenChange={(open) =>
                    onTableFacetChange?.(
                      open
                        ? { kind: "property", property_id: property.id }
                        : null,
                    )
                  }
                >
                  <DropdownMenuSubTrigger>
                    {property.icon ? (
                      <PropertyIcon property={property} className="size-3.5 text-caption" />
                    ) : (
                      <SlidersHorizontal className="size-3.5" />
                    )}
                    <span className="flex-1 truncate">{property.name}</span>
                    {selected.length > 0 && (
                      <span className="text-caption text-primary font-medium">
                        {selected.length}
                      </span>
                    )}
                  </DropdownMenuSubTrigger>
                  <DropdownMenuSubContent className="w-auto min-w-44 p-1">
                    <PropertyFilterOptions
                      property={property}
                      counts={counts.property.get(property.id)}
                      selected={selected}
                      onToggle={(optionId) => act.togglePropertyFilter(property.id, optionId)}
                      onSetValues={(optionIds) => act.setPropertyFilterValues(property.id, optionIds)}
                      fixedIds={viewBaseline?.property.get(property.id)}
                      fixedTitle={fixedTitle}
                    />
                  </DropdownMenuSubContent>
                </DropdownMenuSub>
              );
            })}

            {/* 重置 */}
            {hasActiveFilters && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={() => {
                    if (viewBaseline) {
                      // 在已保存视图里，"重置"回到该视图自身的条件，而不是清空一切。
                      act.resetFiltersTo(viewBaseline.raw);
                    } else {
                      act.clearFilters();
                    }
                    onDateFilterChange?.(null);
                  }}
                >
                  {t(($) => $.filters.reset)}
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function IssueDisplayControls({
  scopedIssues,
  hideViewToggle = false,
  allowGantt = false,
  dateFilter = null,
  onDateFilterChange,
  facetCountsExact = true,
  tableFacetCounts,
  onTableFacetChange,
  viewBaseline,
}: {
  scopedIssues: Issue[];
  hideViewToggle?: boolean;
  dateFilter?: IssueDateFilter | null;
  onDateFilterChange?: (filter: IssueDateFilter | null) => void;
  /** 已打开的保存视图：菜单把视图已固定的值标成勾选并禁用；
   *  触发器上的角标数字只统计叠加在视图之上的增补。 */
  viewBaseline?: IssueViewBaseline;
  // 只有项目详情页会渲染 <GanttView>；其他宿主（全局 /issues、/my-issues、
  // 成员面板）遇到 viewMode === "gantt" 时会悄悄回退成列表——若在那里暴露
  // 甘特选项，就会出现"选了却没反应"。因此甘特保持显式开启（opt-in）。
  allowGantt?: boolean;
  /**
   * `scopedIssues` 是否已覆盖该 surface 的完整数据窗口。表格视图不拿已加载行
   * 来计数；服务端分页的列表/看板/泳道也遵循同一规则。它们改为从后端获取
   * `tableFacetCounts`，让角标保持精确，同时无需把全部问题下载到前端。
   */
  facetCountsExact?: boolean;
  tableFacetCounts?: IssueTableFacetsResponse;
  onTableFacetChange?: (facet: IssueTableFacetSpec | null) => void;
}) {
  const { t } = useT("issues");
  const [tableGroupMenuOpen, setTableGroupMenuOpen] = useState(false);
  const [viewMenuOpen, setViewMenuOpen] = useState(false);
  const viewMode = useViewStore((s) => s.viewMode);
  const statusFilters = useViewStore((s) => s.statusFilters);
  const priorityFilters = useViewStore((s) => s.priorityFilters);
  const assigneeFilters = useViewStore((s) => s.assigneeFilters);
  const includeNoAssignee = useViewStore((s) => s.includeNoAssignee);
  const creatorFilters = useViewStore((s) => s.creatorFilters);
  const projectFilters = useViewStore((s) => s.projectFilters);
  const includeNoProject = useViewStore((s) => s.includeNoProject);
  const labelFilters = useViewStore((s) => s.labelFilters);
  const propertyFilters = useViewStore((s) => s.propertyFilters);
  const cardPropertyIds = useViewStore((s) => s.cardPropertyIds);
  const sortBy = useViewStore((s) => s.sortBy);
  const sortDirection = useViewStore((s) => s.sortDirection);
  const grouping = useViewStore((s) => s.grouping);
  const swimlaneGrouping = useViewStore((s) => s.swimlaneGrouping);
  const cardProperties = useViewStore((s) => s.cardProperties);
  const tableGrouping = useViewStore((s) => s.tableGrouping ?? "none");
  const tableHierarchy = useViewStore((s) => s.tableHierarchy ?? true);
  const showSubIssues = useViewStore((s) => s.showSubIssues);
  const act = useViewStoreApi().getState();
  const headerWsId = useWorkspaceId();
  // 生效中的自定义属性目录：驱动下方的筛选区、动态排序/分组选项
  // 以及卡片属性的开关列表。
  const { data: workspaceProperties = [] } = useQuery(propertyListOptions(headerWsId));
  const propertyById = useMemo(
    () => new Map(workspaceProperties.map((p) => [p.id, p])),
    [workspaceProperties],
  );
  const filterableProperties = useMemo(
    () =>
      workspaceProperties.filter((p) => isFilterablePropertyType(p.type)),
    [workspaceProperties],
  );
  const sortableProperties = useMemo(
    () => workspaceProperties.filter((p) => p.type === "number" || p.type === "date"),
    [workspaceProperties],
  );
  const groupableProperties = useMemo(
    () => workspaceProperties.filter((p) => p.type === "select"),
    [workspaceProperties],
  );
  const tableGroupableProperties = useMemo(
    () =>
      workspaceProperties.filter((p) =>
        ["select", "checkbox"].includes(p.type),
      ),
    [workspaceProperties],
  );

  const showDateFilter = !!onDateFilterChange;

  // 只统计定义仍有效的筛选——钉在已归档定义上的筛选会被 surface 控制器剥离，
  // 不能让一个"已不存在"的筛选点亮角标数字。
  const effectivePropertyFilters = useMemo(() => {
    const activeIds = new Set(filterableProperties.map((p) => p.id));
    return Object.fromEntries(
      Object.entries(propertyFilters).filter(([id, selected]) => selected.length > 0 && activeIds.has(id)),
    );
  }, [filterableProperties, propertyFilters]);

  const activeFilterCount = getActiveFilterCount(
    {
      statusFilters,
      priorityFilters,
      propertyFilters: effectivePropertyFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      projectFilters,
      includeNoProject,
      labelFilters,
      dateFilter: showDateFilter ? dateFilter : null,
    },
    viewBaseline,
  );
  const hasActiveFilters = activeFilterCount > 0;

  const SORT_LABEL_KEY: Record<typeof SORT_OPTIONS[number]["value"], "sort_manual" | "sort_status" | "sort_priority" | "sort_start_date" | "sort_due_date" | "sort_created" | "sort_updated" | "sort_title"> = {
    position: "sort_manual",
    status: "sort_status",
    priority: "sort_priority",
    start_date: "sort_start_date",
    due_date: "sort_due_date",
    created_at: "sort_created",
    updated_at: "sort_updated",
    title: "sort_title",
  };
  const GROUPING_LABEL_KEY: Record<typeof GROUPING_OPTIONS[number]["value"], "group_status" | "group_assignee" | "group_project"> = {
    status: "group_status",
    assignee: "group_assignee",
    project: "group_project",
  };
  const SWIMLANE_GROUPING_LABEL_KEY: Record<SwimlaneGrouping, "group_parent" | "group_project" | "group_assignee"> = {
    parent: "group_parent",
    project: "group_project",
    assignee: "group_assignee",
  };
  const CARD_PROPERTY_LABEL_KEY: Record<typeof CARD_PROPERTY_OPTIONS[number]["key"], "card_priority" | "card_description" | "card_assignee" | "card_start_date" | "card_due_date" | "card_project" | "card_labels" | "card_child_progress"> = {
    priority: "card_priority",
    description: "card_description",
    assignee: "card_assignee",
    startDate: "card_start_date",
    dueDate: "card_due_date",
    project: "card_project",
    labels: "card_labels",
    childProgress: "card_child_progress",
  };
  const sortPropertyId = propertyIdFromViewKey(sortBy);
  const groupingPropertyId = propertyIdFromViewKey(grouping);
  const tableGroupingPropertyId = propertyIdFromViewKey(tableGrouping);
  // 属性型排序/分组的标签直接取属性定义名；持久化的 id 若已失效
  //（定义被归档/删除），回退到"手动/按状态"等默认标签。
  const sortLabel = sortPropertyId
    ? propertyById.get(sortPropertyId)?.name ?? t(($) => $.display.sort_manual)
    : t(($) => $.display[SORT_LABEL_KEY[sortBy as keyof typeof SORT_LABEL_KEY]]);
  const groupingLabel = groupingPropertyId
    ? propertyById.get(groupingPropertyId)?.name ?? t(($) => $.display.group_status)
    : t(($) => $.display[GROUPING_LABEL_KEY[grouping as keyof typeof GROUPING_LABEL_KEY]]);
  const swimlaneGroupingLabel = t(($) => $.display[SWIMLANE_GROUPING_LABEL_KEY[swimlaneGrouping]]);
  const effectiveTableGrouping =
    tableGroupingPropertyId && !propertyById.has(tableGroupingPropertyId)
      ? "none"
      : tableGrouping;
  const tableGroupingLabel = tableGroupingPropertyId
    ? propertyById.get(tableGroupingPropertyId)?.name ??
      t(($) => $.table.group_none)
    : effectiveTableGrouping === "status"
      ? t(($) => $.table.columns.status)
      : effectiveTableGrouping === "assignee"
        ? t(($) => $.table.columns.assignee)
        : effectiveTableGrouping === "project"
          ? t(($) => $.table.columns.project)
          : t(($) => $.table.group_none);
  const controlButtonClass = "h-8 w-8 gap-1 px-0 text-muted-foreground md:h-7 md:w-auto md:px-2.5";

  return (
    <div className="flex shrink-0 items-center gap-1">
        <IssueFilterMenu
          trigger={
            <Button
              variant={hasActiveFilters ? "default" : "outline"}
              size="sm"
              className={
                hasActiveFilters
                  ? "h-8 w-8 gap-1 bg-brand px-0 text-white hover:bg-brand/90 md:h-7 md:w-auto md:px-2.5"
                  : controlButtonClass
              }
            >
              <Filter className="size-3.5" />
              <span className="hidden md:inline">
                {hasActiveFilters
                  ? t(($) => $.filters.active_count, { count: activeFilterCount })
                  : t(($) => $.filters.tooltip)}
              </span>
              {hasActiveFilters && (
                <span className="tabular-nums md:hidden">{activeFilterCount}</span>
              )}
            </Button>
          }
          tooltip={t(($) => $.filters.tooltip)}
          scopedIssues={scopedIssues}
          facetCountsExact={facetCountsExact}
          tableFacetCounts={tableFacetCounts}
          onTableFacetChange={onTableFacetChange}
          dateFilter={showDateFilter ? dateFilter : null}
          onDateFilterChange={onDateFilterChange}
          viewBaseline={viewBaseline}
        />

        {viewMode === "table" && (
          <DropdownMenu
            open={tableGroupMenuOpen}
            onOpenChange={setTableGroupMenuOpen}
          >
            <DropdownMenuTrigger
              render={
                <Button
                  variant="outline"
                  size="sm"
                  className={controlButtonClass}
                >
                  <Rows3 className="size-3.5" />
                  <span className="hidden md:inline">
                    {effectiveTableGrouping === "none"
                      ? t(($) => $.table.group_label)
                      : t(($) => $.table.group_active, {
                          group: tableGroupingLabel,
                        })}
                  </span>
                  <ChevronDown className="size-3 text-muted-foreground" />
                </Button>
              }
            />
            <DropdownMenuContent align="end" className="w-auto min-w-48">
              <DropdownMenuRadioGroup
                value={effectiveTableGrouping}
                onValueChange={(value) => {
                  act.setTableGrouping(value as TableGrouping);
                  setTableGroupMenuOpen(false);
                }}
              >
                <DropdownMenuRadioItem value="none">
                  {t(($) => $.table.group_none)}
                </DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="status">
                  {t(($) => $.table.columns.status)}
                </DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="assignee">
                  {t(($) => $.table.columns.assignee)}
                </DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="project">
                  {t(($) => $.table.columns.project)}
                </DropdownMenuRadioItem>
                {tableGroupableProperties.map((property) => (
                  <DropdownMenuRadioItem
                    key={property.id}
                    value={`property:${property.id}`}
                  >
                    <PropertyIcon
                      property={property}
                      className="size-3.5 text-caption"
                    />
                    <span>{property.name}</span>
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        )}

        {viewMode === "table" && (
          <TableColumnPicker
            properties={workspaceProperties}
            trigger={
              <Button
                variant="outline"
                size="sm"
                className={controlButtonClass}
              >
                <Columns3 className="size-3.5" />
                <span className="hidden md:inline">
                  {t(($) => $.table.columns.section)}
                </span>
                <ChevronDown className="size-3 text-muted-foreground" />
              </Button>
            }
          />
        )}

        {/* 显示设置 */}
        <Popover>
          <Tooltip>
            <PopoverTrigger
              render={
                <TooltipTrigger
                  render={
                    <Button variant="outline" size="sm" className={controlButtonClass}>
                      <SlidersHorizontal className="size-3.5" />
                      <span className="hidden md:inline">{t(($) => $.display.button)}</span>
                    </Button>
                  }
                />
              }
            />
            <TooltipContent side="bottom">{t(($) => $.display.tooltip)}</TooltipContent>
          </Tooltip>
          <PopoverContent align="end" className="w-72 p-3">
            <div className="space-y-3">
              {/* 标签与控件左右排列，多控件区域纵向排列以适配弹层宽度。 */}
              {viewMode === "board" && (
                <div className="flex items-center justify-between gap-3">
                  <span className="text-caption font-medium text-muted-foreground">
                    {t(($) => $.display.grouping_section)}
                  </span>
                  <Select
                    items={[
                      ...GROUPING_OPTIONS.map((opt) => ({
                        value: opt.value as string,
                        label: t(($) => $.display[GROUPING_LABEL_KEY[opt.value]]),
                      })),
                      ...groupableProperties.map((p) => ({
                        value: `property:${p.id}`,
                        label: p.name,
                      })),
                    ]}
                    value={grouping}
                    onValueChange={(v) => {
                      if (v) act.setGrouping(v as IssueGrouping);
                    }}
                  >
                    <SelectTrigger size="sm" className="w-32" aria-label={t(($) => $.display.grouping_section)}>
                      <SelectValue>{groupingLabel}</SelectValue>
                    </SelectTrigger>
                    <SelectContent align="end">
                      <SelectGroup>
                      {GROUPING_OPTIONS.map((opt) => (
                        <SelectItem key={opt.value} value={opt.value}>
                          {t(($) => $.display[GROUPING_LABEL_KEY[opt.value]])}
                        </SelectItem>
                      ))}
                      {groupableProperties.map((property) => (
                        <SelectItem key={property.id} value={`property:${property.id}`}>
                          {property.name}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                    </SelectContent>
                  </Select>
                </div>
              )}
              {viewMode === "swimlane" && (
                <div className="flex items-center justify-between gap-3">
                  <span className="text-caption font-medium text-muted-foreground">
                    {t(($) => $.display.grouping_section)}
                  </span>
                  <Select
                    items={SWIMLANE_GROUPINGS.map((value) => ({
                      value: value as string,
                      label: t(($) => $.display[SWIMLANE_GROUPING_LABEL_KEY[value]]),
                    }))}
                    value={swimlaneGrouping}
                    onValueChange={(v) => {
                      if (v) act.setSwimlaneGrouping(v as SwimlaneGrouping);
                    }}
                  >
                    <SelectTrigger size="sm" className="w-32" aria-label={t(($) => $.display.grouping_section)}>
                      <SelectValue>{swimlaneGroupingLabel}</SelectValue>
                    </SelectTrigger>
                    <SelectContent align="end">
                      <SelectGroup>
                      {SWIMLANE_GROUPINGS.map((value) => (
                        <SelectItem key={value} value={value}>
                          {t(($) => $.display[SWIMLANE_GROUPING_LABEL_KEY[value]])}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                    </SelectContent>
                  </Select>
                </div>
              )}
              {viewMode === "table" && (
                <label className="flex cursor-pointer items-center justify-between gap-3">
                  <span className="min-w-0">
                    <span className="block text-caption font-medium text-muted-foreground">
                      {t(($) => $.table.hierarchy)}
                    </span>
                    <span className="block text-caption text-faint-foreground">
                      {t(($) => $.table.hierarchy_description)}
                    </span>
                  </span>
                  <Switch
                    size="sm"
                    checked={tableHierarchy}
                    onCheckedChange={() => act.toggleTableHierarchy()}
                  />
                </label>
              )}
              <div>
                <span className="text-caption font-medium text-muted-foreground">
                  {t(($) => $.display.ordering_section)}
                </span>
                {/* Direction labels run up to "Reverse workflow order", so the
                    pair gets the full popover width (w-72 keeps "Status" beside
                    it untruncated): the field select absorbs the slack and
                    truncates, the direction label never does. */}
                <div className="mt-2 flex items-center gap-1.5">
                  <Select
                    items={[
                      ...SORT_OPTIONS.map((opt) => ({
                        value: opt.value as string,
                        label: t(($) => $.display[SORT_LABEL_KEY[opt.value as keyof typeof SORT_LABEL_KEY]]),
                      })),
                      ...sortableProperties.map((p) => ({
                        value: `property:${p.id}`,
                        label: p.name,
                      })),
                    ]}
                    value={sortBy}
                    onValueChange={(v) => {
                      if (v) act.setSortBy(v as SortField);
                    }}
                  >
                    <SelectTrigger size="sm" className="min-w-0 flex-1" aria-label={t(($) => $.display.ordering_section)}>
                      <SelectValue>{sortLabel}</SelectValue>
                    </SelectTrigger>
                    <SelectContent align="end">
                      <SelectGroup>
                      {SORT_OPTIONS.map((opt) => (
                        <SelectItem key={opt.value} value={opt.value}>
                          {t(($) => $.display[SORT_LABEL_KEY[opt.value as keyof typeof SORT_LABEL_KEY]])}
                        </SelectItem>
                      ))}
                      {sortableProperties.map((property) => (
                        <SelectItem key={property.id} value={`property:${property.id}`}>
                          {property.name}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                    </SelectContent>
                  </Select>
                  {sortBy !== "position" && (
                    <Button
                      variant="outline"
                      size="icon-sm"
                      onClick={() =>
                        act.setSortDirection(sortDirection === "asc" ? "desc" : "asc")
                      }
                      title={sortDirection === "asc" ? t(($) => $.display.ascending_title) : t(($) => $.display.descending_title)}
                    >
                      {sortDirection === "asc" ? (
                        <ArrowUp className="size-3.5" />
                      ) : (
                        <ArrowDown className="size-3.5" />
                      )}
                    </Button>
                  )}
                </div>
              </div>
              <label className="flex cursor-pointer items-center justify-between gap-3">
                <span className="text-caption font-medium text-muted-foreground">
                  {t(($) => $.display.show_sub_issues)}
                </span>
                <Switch
                  size="sm"
                  checked={showSubIssues}
                  onCheckedChange={() => act.toggleShowSubIssues()}
                />
              </label>
              {viewMode !== "table" && (
                <div>
                  <span className="text-caption font-medium text-muted-foreground">
                    {t(($) => $.display.card_properties_section)}
                  </span>
                  {/* chip 式开关（按下 = 卡片上展示该属性）。未按下的 chip
                      变淡，让已启用集合一眼可辨。 */}
                  <div className="mt-2 flex flex-wrap gap-1">
                    {CARD_PROPERTY_OPTIONS.map((opt) => (
                      <Toggle
                        key={opt.key}
                        size="sm"
                        variant="outline"
                        pressed={cardProperties[opt.key]}
                        onPressedChange={() => act.toggleCardProperty(opt.key)}
                        className="h-6 rounded-md px-2 text-caption text-muted-foreground hover:bg-muted/60 aria-pressed:border-transparent aria-pressed:bg-accent aria-pressed:text-accent-foreground aria-pressed:hover:bg-accent/80"
                      >
                        {t(($) => $.display[CARD_PROPERTY_LABEL_KEY[opt.key]])}
                      </Toggle>
                    ))}
                    {workspaceProperties.map((property) => (
                      <Toggle
                        key={property.id}
                        size="sm"
                        variant="outline"
                        pressed={cardPropertyIds.includes(property.id)}
                        onPressedChange={() => act.toggleCardPropertyId(property.id)}
                        className="h-6 max-w-40 rounded-md px-2 text-caption text-muted-foreground hover:bg-muted/60 aria-pressed:border-transparent aria-pressed:bg-accent aria-pressed:text-accent-foreground aria-pressed:hover:bg-accent/80"
                      >
                        <span className="truncate">{property.name}</span>
                      </Toggle>
                    ))}
                  </div>
                </div>
              )}
            </div>
          </PopoverContent>
        </Popover>

        {/* 视图切换。若 store 里持久化了 `viewMode === "gantt"` 但当前
            surface 不渲染甘特，就回退成"列表"，让触发器图标与屏幕
            实际展示的视图一致。 */}
        {!hideViewToggle && (
          <DropdownMenu open={viewMenuOpen} onOpenChange={setViewMenuOpen}>
            <Tooltip>
              <DropdownMenuTrigger
                render={
                  <TooltipTrigger
                    render={
                      <Button variant="outline" size="sm" className={controlButtonClass}>
                        {viewMode === "board" ? (
                          <Columns3 className="size-3.5" />
                        ) : viewMode === "table" ? (
                          <Table2 className="size-3.5" />
                        ) : viewMode === "swimlane" ? (
                          <Waves className="size-3.5" />
                        ) : viewMode === "gantt" && allowGantt ? (
                          <ChartGantt className="size-3.5" />
                        ) : (
                          <List className="size-3.5" />
                        )}
                        <span className="hidden md:inline">
                          {viewMode === "board"
                            ? t(($) => $.view.board)
                            : viewMode === "table"
                            ? t(($) => $.view.table)
                            : viewMode === "swimlane"
                            ? t(($) => $.view.swimlane)
                            : viewMode === "gantt" && allowGantt
                            ? t(($) => $.view.gantt)
                            : t(($) => $.view.list)}
                        </span>
                      </Button>
                    }
                  />
                }
              />
              <TooltipContent side="bottom">
                {viewMode === "board"
                  ? t(($) => $.view.tooltip_board)
                  : viewMode === "table"
                  ? t(($) => $.view.tooltip_table)
                  : viewMode === "swimlane"
                  ? t(($) => $.view.tooltip_swimlane)
                  : viewMode === "gantt" && allowGantt
                  ? t(($) => $.view.tooltip_gantt)
                  : t(($) => $.view.tooltip_list)}
              </TooltipContent>
            </Tooltip>
            <DropdownMenuContent align="end" className="w-auto">
              <DropdownMenuGroup>
                <DropdownMenuLabel>{t(($) => $.view.section)}</DropdownMenuLabel>
              </DropdownMenuGroup>
              <DropdownMenuRadioGroup
                value={viewMode}
                onValueChange={(v) => {
                  act.setViewMode(v as ViewMode);
                  setViewMenuOpen(false);
                }}
              >
                <DropdownMenuRadioItem value="board">
                  <Columns3 />
                  {t(($) => $.view.board)}
                </DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="list">
                  <List />
                  {t(($) => $.view.list)}
                </DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="table">
                  <Table2 />
                  {t(($) => $.view.table)}
                </DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="swimlane">
                  <Waves />
                  {t(($) => $.view.swimlane)}
                </DropdownMenuRadioItem>
                {allowGantt && (
                  <DropdownMenuRadioItem value="gantt">
                    <ChartGantt />
                    {t(($) => $.view.gantt)}
                  </DropdownMenuRadioItem>
                )}
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        )}

    </div>
  );
}
