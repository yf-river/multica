"use client";

// 问题界面控制器 hook：把「数据窗口里现在显示什么」的状态——当前视图模式、筛选、
// 排序、按状态/分组的分页游标、服务端 facet 计数、选择集、动作集——全部归拢，
// 导出成一个控制器对象供 IssueSurface 及各视图渲染。它是 board/list/table/gantt/
// swimlane 几套视图共用一份逻辑的中枢。
import { useCallback, useEffect, useMemo, useState } from "react";
import { hashKey, keepPreviousData, useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type {
  Issue,
  IssueStatusCategory,
  IssueTableFacetSpec,
  IssueTableFacetsResponse,
  IssueTableGroupsRequest,
  IssueTableQuerySpec,
  Project,
  WorkingAgentSummary,
} from "@multica/core/types";
import { workspaceWorkingAgentsOptions } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { ALL_STATUSES } from "@multica/core/issues/config";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { statusFilterColumns } from "@multica/core/issues";
import { dateOnlyToLocalDate } from "@multica/core/issues/date";
import type { IssueSortParam } from "@multica/core/issues/queries";
import { issueTableFacetsOptions } from "@multica/core/issues/queries";
import {
  buildIssueSurfaceQueryPlan,
  type IssueSurfaceQueryPlan,
} from "@multica/core/issues/surface/query-plan";
import {
  assigneeTypesForActorKind,
  type IssueScope,
} from "@multica/core/issues/surface/scope";
import type { IssueDateFilter, SortField } from "@multica/core/issues/stores/view-store";
import { propertyListOptions } from "@multica/core/properties";
import { propertyIdFromViewKey } from "@multica/core/issues/stores/view-store";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import type { IssueFilters } from "../utils/filter";
import type { ChildProgress } from "../components/list-row";
import { IssueTableExportIntegrityError } from "../components/table-view-model";
import type { IssueSurfaceMode } from "./types";
import type { IssueSurfaceActions } from "./actions-context";
import {
  type IssueSurfaceSelection,
  useCreateIssueSurfaceSelection,
} from "./selection-context";
import type { IssueCreateDefaults } from "./types";
import {
  useIssueSurfaceActions,
  type MoveIssueUpdates,
} from "./use-issue-surface-actions";
import { useIssueSurfaceData } from "./use-issue-surface-data";
import {
  useIssueStatusBranches,
  type IssueStatusPagination,
} from "./use-issue-status-branches";
import {
  useIssueGroupBranches,
  type IssueGroupBranches,
} from "./use-issue-group-branches";

interface UseIssueSurfaceControllerInput {
  scope: IssueScope;
  modes: IssueSurfaceMode[];
  createDefaults?: IssueCreateDefaults;
  search?: string;
}

/** useIssueSurfaceController 产出的完整控制器。 */
export interface IssueSurfaceController {
  scopeKey: string;
  projectId?: string;
  createDefaults: IssueCreateDefaults;
  viewMode: IssueSurfaceMode;
  allowGantt: boolean;
  surfaceIssues: Issue[];
  projectIssues: Issue[];
  issues: Issue[];
  swimlaneIssues: Issue[];
  /** 正处在本 surface 内、且命中当前激活筛选的智能体——表头 chip 的计数，
   *  点它正好留下这些智能体的行（MUL-4884, MUL-5525）。`undefined` 表示投影还没
   *  落定；chip 渲染的是不确定状态，而不是一个站不住脚的数字。 */
  workingAgents: WorkingAgentSummary[] | undefined;
  filteredGanttIssues: Issue[];
  sort: IssueSortParam;
  ganttIssues: Issue[];
  visibleStatuses: IssueStatusCategory[];
  hiddenStatuses: IssueStatusCategory[];
  /** 服务端精确计数 + 列表/按状态看板的游标控制。 */
  statusPagination?: IssueStatusPagination;
  /** 精确的分组目录 + 各分组独立行游标（用于按指派人/自定义属性看板与复合泳道格子）。 */
  groupBranches?: IssueGroupBranches;
  activeFilters: Omit<IssueFilters, "statusFilters">;
  /** 任何能被 `clearFilters()` 重置的筛选处于开启状态。让空 surface 能说
   *  「你的筛选藏起了所有内容」而不是「这里什么都没有」。 */
  hasActiveFilters: boolean;
  actions: IssueSurfaceActions;
  selection: IssueSurfaceSelection;
  childProgressMap: Map<string, ChildProgress>;
  projectMap: Map<string, Project>;
  resolveTableExportLookups: (needs: {
    projects: boolean;
    childProgress: boolean;
  }) => Promise<{
    projectMap: Map<string, Project>;
    childProgressMap: Map<string, ChildProgress>;
  }>;
  tableSearch: string;
  /** 规范的、服务端拥有的 Table 会员集合。 */
  tableQuerySpec: IssueTableQuerySpec;
  /** 当前激活的、服务端支撑的筛选子菜单的精确析取计数。 */
  tableFacetCounts?: IssueTableFacetsResponse;
  /** 本地计数使用中，scopedIssues 是否就是完整的客户端窗口。 */
  facetCountsExact: boolean;
  /** 当某个筛选子菜单被打开时，加载对应的那一个服务端 facet。 */
  setActiveTableFacet: (facet: IssueTableFacetSpec | null) => void;
  setTableSearch: (query: string) => void;
  exportTableIssues: () => Promise<Issue[]>;
  isLoading: boolean;
  /** 见 IssueSurfaceData.isRefreshing——基于占位数据的后台重校验。 */
  isRefreshing: boolean;
  isEmpty: boolean;
  /**
   * 自定义状态筛选所依赖的状态目录加载失败。该筛选无法被兑现，
   * 所以 surface 显示一个可重试的错误，而不是一个无法解释的空看板。（MUL-6243）
   */
  isStatusCatalogError: boolean;
  /** 重跑失败的状态目录请求（配合 {@link isStatusCatalogError}）。 */
  retryStatusCatalog: () => void;
  openCreateIssue: (defaults?: IssueCreateDefaults) => void;
  moveIssue: (
    issueId: string,
    updates: MoveIssueUpdates,
    onSettled?: () => void,
  ) => void;
}

function issueDateFilterToApiParams(filter: IssueDateFilter | null) {
  if (!filter) return {};

  const from = dateOnlyToLocalDate(filter.from);
  const to = dateOnlyToLocalDate(filter.to);
  if (!from || !to) return {};

  const start = from <= to ? from : to;
  const endSource = from <= to ? to : from;
  const end = new Date(endSource);
  end.setDate(end.getDate() + 1);

  return {
    date_field: filter.field,
    date_start: start.toISOString(),
    date_end: end.toISOString(),
  };
}

function useDebouncedTableSearch(value: string, delayMs = 250) {
  const [debouncedValue, setDebouncedValue] = useState(value.trim());

  useEffect(() => {
    const timer = window.setTimeout(
      () => setDebouncedValue(value.trim()),
      delayMs,
    );
    return () => window.clearTimeout(timer);
  }, [delayMs, value]);

  return debouncedValue;
}

/** 所有未落定的列表查询共用的同一份空数组引用。
 *
 * `const { data = [] } = useQuery(...)` 在查询还没有数据期间每次渲染都会分配一个新数组
 * ——这正好覆盖切换工作区后的整段窗口。下游拿那个数组当 memo 依赖时，光是这个空默认值
 * 就足以让派生的 Set、table query spec 和分支查询列表每渲染一次重建一遍（MUL-5477）。 */
const EMPTY_LIST: never[] = [];

/**
 * 把派生值的引用身份「钉」到它的内容上。
 *
 * `tableQuerySpec` 由 17 个依赖重建，其中任何一个失去引用稳定性，都会在查询没变时
 * 给所有消费者发一个新的对象。消费者拿它当 memo 依赖，Table 还把它当作 `useQueries`
 * 列表的来源——所以一份「变了但相等」的 spec 会让列表每次渲染都重建。
 *
 * 用 TanStack 自己的 `hashKey` 而不是 `JSON.stringify`，是为了和该 spec 被哈希进
 * `queryKey` 的方式完全一致：对象键会排序，所以两条最终落到同一次查询的 spec，
 * 在这里也只会得到一个身份。
 */
function useStableByContent<T>(value: T): T {
  const contentKey = hashKey([value]);
  // eslint-disable-next-line react-hooks/exhaustive-deps -- identity follows the content hash, not the reference
  return useMemo(() => value, [contentKey]);
}

/**
 * 问题界面控制器 hook：按 scope 归拢视图模式、筛选、排序、服务端分支分页、facet 计数、
 * 选择与动作，产出驱动 IssueSurface 渲染的一切状态与方法。
 * @param input.scope - 数据范围（工作区/我的/项目/成员/团队）。
 * @param input.modes - 允许的视图模式集合。
 * @param input.createDefaults - 创建问题弹窗的默认值。
 * @param input.search - 搜索词（非 Table 界面使用）。
 * @returns 完整的 surface 控制器。
 */
export function useIssueSurfaceController({
  scope,
  modes,
  createDefaults,
  search = "",
}: UseIssueSurfaceControllerInput): IssueSurfaceController {
  const wsId = useWorkspaceId();
  const queryPlan = useMemo<IssueSurfaceQueryPlan>(
    () => buildIssueSurfaceQueryPlan(scope),
    [scope],
  );
  const scopeKey = queryPlan.scopeKey;
  const projectId = scope.type === "project" ? scope.projectId : undefined;

  const viewMode = useViewStore((s) => s.viewMode);
  const setViewMode = useViewStore((s) => s.setViewMode);
  const grouping = useViewStore((s) => s.grouping);
  const sortBy = useViewStore((s) => s.sortBy);
  const sortDirection = useViewStore((s) => s.sortDirection);
  const dateFilter = useViewStore((s) => s.dateFilter);
  const statusFilters = useViewStore((s) => s.statusFilters);
  const priorityFilters = useViewStore((s) => s.priorityFilters);
  const assigneeFilters = useViewStore((s) => s.assigneeFilters);
  const includeNoAssignee = useViewStore((s) => s.includeNoAssignee);
  const creatorFilters = useViewStore((s) => s.creatorFilters);
  const projectFilters = useViewStore((s) => s.projectFilters);
  const includeNoProject = useViewStore((s) => s.includeNoProject);
  const labelFilters = useViewStore((s) => s.labelFilters);
  const propertyFilters = useViewStore((s) => s.propertyFilters);
  const agentRunningFilter = useViewStore((s) => s.agentRunningFilter);
  const showSubIssues = useViewStore((s) => s.showSubIssues);
  const ganttShowCompleted = useViewStore((s) => s.ganttShowCompleted);
  const cardProperties = useViewStore((s) => s.cardProperties);
  const swimlaneGrouping = useViewStore((s) => s.swimlaneGrouping);
  const tableColumns = useViewStore((s) => s.tableColumns);
  const tableGrouping = useViewStore((s) => s.tableGrouping);
  const listCollapsedStatuses = useViewStore((s) => s.listCollapsedStatuses);
  const hiddenStatusCategories = useViewStore((s) => s.hiddenStatusCategories);
  const catalog = useIssueStatuses(wsId);
  const { hasCustomStatuses } = catalog;
  const [tableSearch, setTableSearch] = useState("");

  const allowedModes = useMemo(() => new Set<IssueSurfaceMode>(modes), [modes]);
  const fallbackMode = modes[0] ?? "list";
  const effectiveViewMode = allowedModes.has(viewMode as IssueSurfaceMode)
    ? (viewMode as IssueSurfaceMode)
    : fallbackMode;

  useEffect(() => {
    if (!allowedModes.has(viewMode as IssueSurfaceMode)) {
      setViewMode(fallbackMode);
    }
  }, [allowedModes, fallbackMode, setViewMode, viewMode]);

  const resolvedCreateDefaults = useMemo(
    () => ({ ...queryPlan.createDefaults, ...createDefaults }),
    [createDefaults, queryPlan.createDefaults],
  );

  const dateParams = useMemo(
    () => issueDateFilterToApiParams(dateFilter),
    [dateFilter],
  );
  // 当前生效的自定义属性目录。持久化的视图状态可能比定义活得更久（归档/删除）：
  // 指向已失效定义的筛选会在进入判定函数前被剥掉；指向已失效定义的排序退化为手动顺序
  // ——与表头已经显示的一致。
  const { data: workspaceProperties = EMPTY_LIST, isSuccess: catalogSettled } = useQuery(propertyListOptions(wsId));
  const activePropertyIds = useMemo(
    () => new Set(workspaceProperties.map((p) => p.id)),
    [workspaceProperties],
  );
  const effectivePropertyFilters = useMemo(() => {
    // 目录还在加载（或失败）时，持久化的筛选原样放行：把冷目录当成「确认为空」
    // 会在首屏悄悄丢掉用户的筛选（clean-room review F6）。旧服务器会 404 到一个
    // 已落定的空目录，所以下面的剥除逻辑仍能保护那条路径。
    if (!catalogSettled) return propertyFilters;
    const entries = Object.entries(propertyFilters).filter(
      ([propertyId, selected]) => selected.length > 0 && activePropertyIds.has(propertyId),
    );
    if (entries.length === Object.keys(propertyFilters).length) return propertyFilters;
    return Object.fromEntries(entries);
  }, [activePropertyIds, catalogSettled, propertyFilters]);

  // 自定义属性的排序与筛选都由后端处理：排序参数携带 `property:<id>`（服务端把它编成
  // 类型化 ORDER BY 表达式）、数据窗口包携带属性筛选，所以跨分页结果都正确——不只是
  // 已加载窗口内的结果。钉到已失效定义的排序退化为按 position 排序。
  const rawPropertySortId = propertyIdFromViewKey(sortBy);
  const propertySortId =
    rawPropertySortId && (!catalogSettled || activePropertyIds.has(rawPropertySortId))
      ? rawPropertySortId
      : null;
  const sort = useMemo<IssueSortParam>(() => {
    const sortBy_: IssueSortParam["sort_by"] = propertySortId
      ? `property:${propertySortId}`
      : rawPropertySortId
        ? "position"
        : (sortBy as Exclude<SortField, `property:${string}`>);
    return {
      sort_by: sortBy_,
      sort_direction: sortBy_ !== "position" ? sortDirection : undefined,
      ...dateParams,
      ...(Object.keys(effectivePropertyFilters).length > 0
        ? { properties: effectivePropertyFilters }
        : {}),
    };
  }, [dateParams, effectivePropertyFilters, propertySortId, rawPropertySortId, sortBy, sortDirection]);

  const groupingPropertyId = propertyIdFromViewKey(grouping);
  const activeGroupingProperty = groupingPropertyId
    ? workspaceProperties.find(
        (property) =>
          property.id === groupingPropertyId && property.type === "select",
      ) ?? null
    : null;
  const effectiveGrouping =
    groupingPropertyId && catalogSettled && !activeGroupingProperty
      ? "status"
      : grouping;
  const usesGantt = effectiveViewMode === "gantt" && !!projectId;
  const usesTable = effectiveViewMode === "table";
  const activeSearch = usesTable ? tableSearch : search;
  const debouncedActiveSearch = useDebouncedTableSearch(activeSearch);
  const usesServerStatusSurface =
    effectiveViewMode === "list" ||
    (effectiveViewMode === "board" && effectiveGrouping === "status");
  const usesServerGroupSurface =
    (effectiveViewMode === "board" && effectiveGrouping !== "status") ||
    effectiveViewMode === "swimlane";
  const usesServerFacets =
    usesTable || usesServerStatusSurface || usesServerGroupSurface;
  const statusColumnsForFilters = useMemo(
    () => statusFilterColumns(statusFilters, catalog),
    [catalog, statusFilters],
  );
  /**
   * 自定义状态筛选在目录返回前无法路由。目录 pending 时 surface 必须保持加载态，
   * 失败时必须给出可重试的错误——而不能拉取零个分支、渲染出一个空看板，
   * 那正是「只返回空列」会产生的后果。（MUL-6243）
   */
  const statusFilterPending = statusColumnsForFilters.state === "pending";
  const statusFilterError = statusColumnsForFilters.state === "error";
  /**
   * 筛选解析前，拉取被挂起。不只是「收敛成空」：筛选没解析时可见列集合会回退成
   * 全部分类，这时照常拉取会让打开了一个已保存 `qa` 视图的人，短暂看到未筛选的看板。
   * 把请求和加载态都按住是唯一诚实的做法。
   */
  const statusFilterUnresolved = statusFilterPending || statusFilterError;

  // 列是「分类」。有两条独立的东西会收窄它们，把它们混为一谈正是「隐藏待办列」也会
  // 丢掉其他分类里所有自定义状态的 bug 来源：`hiddenStatusCategories` 是显示状态，
  // `statusFilters` 是对具体状态 KEY 的筛选，我们要把它映射回那些 KEY 落在的列。
  // （MUL-6243）
  const serverStatuses = useMemo<IssueStatusCategory[]>(
    () => {
      const selected =
        statusFilters.length > 0 && statusColumnsForFilters.state === "resolved"
          ? statusColumnsForFilters.columns
          : null;
      const visible = ALL_STATUSES.filter(
        (category) =>
          !hiddenStatusCategories.includes(category) &&
          (selected === null || selected.has(category)),
      );
      return effectiveViewMode === "list"
        ? visible.filter((status) => !listCollapsedStatuses.includes(status))
        : visible;
    },
    [
      effectiveViewMode,
      hiddenStatusCategories,
      listCollapsedStatuses,
      statusColumnsForFilters,
      statusFilters,
    ],
  );

  const projectFilterState = useMemo(
    () => ({
      projectFilters: scope.type === "project" ? [] : projectFilters,
      includeNoProject: scope.type === "project" ? false : includeNoProject,
    }),
    [includeNoProject, projectFilters, scope.type],
  );
  const { projectFilters: viewProjectFilters, includeNoProject: viewIncludeNoProject } =
    projectFilterState;

  // 恰好是 `clearFilters()` 会重置的那批筛选，所以一个报告「筛选藏起了一切」的空
  // surface 总能提供一个修好它的按钮。显示开关（显示子问题）和每个 surface 自己的
  // 搜索被刻意排除在外：它们各自有独立的入口，清除筛选并不会撤销它们。
  const hasActiveFilters =
    statusFilters.length > 0 ||
    priorityFilters.length > 0 ||
    assigneeFilters.length > 0 ||
    includeNoAssignee ||
    creatorFilters.length > 0 ||
    viewProjectFilters.length > 0 ||
    viewIncludeNoProject ||
    labelFilters.length > 0 ||
    Object.keys(effectivePropertyFilters).length > 0 ||
    dateFilter != null ||
    agentRunningFilter === true;

  const workingAgentMineRelation =
    scope.type === "my"
      ? scope.relation === "all"
        ? "any"
        : scope.relation
      : undefined;
  const { data: workspaceWorkingAgents = EMPTY_LIST } = useQuery(
    workspaceWorkingAgentsOptions(wsId, "issue", workingAgentMineRelation),
  );
  const workingIssueIDs = useMemo(() => {
    const issueIDs = new Set<string>();
    for (const agent of workspaceWorkingAgents) {
      for (const issueID of agent.issue_ids) issueIDs.add(issueID);
    }
    return issueIDs;
  }, [workspaceWorkingAgents]);

  const derivedTableQuerySpec = useMemo<IssueTableQuerySpec>(() => {
    let queryScope: IssueTableQuerySpec["scope"];
    switch (scope.type) {
      case "workspace": {
        const assigneeTypes = assigneeTypesForActorKind(scope.actorKind);
        queryScope = {
          kind: "workspace",
          ...(assigneeTypes ? { assignee_types: assigneeTypes } : {}),
        };
        break;
      }
      case "project": {
        const assigneeTypes = assigneeTypesForActorKind(scope.actorKind);
        queryScope = {
          kind: "project",
          project_id: scope.projectId,
          ...(assigneeTypes ? { assignee_types: assigneeTypes } : {}),
        };
        break;
      }
      case "my":
        queryScope = {
          kind: "my",
          relation: scope.relation === "all" ? "any" : scope.relation,
        };
        break;
      case "actor":
        queryScope = {
          kind: scope.relation === "assigned" ? "assignee" : "creator",
          actor: { type: scope.actorType, id: scope.actorId },
        };
        break;
      case "team":
        throw new Error("Team issue scope is not supported by the Table query");
    }

    const date =
      dateParams.date_field && dateParams.date_start && dateParams.date_end
        ? {
            field: dateParams.date_field,
            start: dateParams.date_start,
            end: dateParams.date_end,
          }
        : undefined;
    return {
      scope: queryScope,
      filters: {
        ...(statusFilters.length > 0 ? { statuses: statusFilters } : {}),
        ...(priorityFilters.length > 0 ? { priorities: priorityFilters } : {}),
        ...(assigneeFilters.length > 0 ? { assignees: assigneeFilters } : {}),
        ...(includeNoAssignee ? { include_no_assignee: true } : {}),
        ...(creatorFilters.length > 0 ? { creators: creatorFilters } : {}),
        ...(viewProjectFilters.length > 0
          ? { project_ids: viewProjectFilters }
          : {}),
        ...(viewIncludeNoProject ? { include_no_project: true } : {}),
        ...(labelFilters.length > 0 ? { label_ids: labelFilters } : {}),
        ...(Object.keys(effectivePropertyFilters).length > 0
          ? { properties: effectivePropertyFilters }
          : {}),
        ...(date ? { date } : {}),
        ...(agentRunningFilter
          ? { working_issue_ids: [...workingIssueIDs] }
          : {}),
        include_sub_issues: showSubIssues,
      },
      ...(debouncedActiveSearch ? { search: debouncedActiveSearch } : {}),
      sort: {
        field: sort.sort_by ?? "position",
        direction: sort.sort_direction ?? "asc",
      },
    };
  }, [
    agentRunningFilter,
    assigneeFilters,
    creatorFilters,
    dateParams,
    debouncedActiveSearch,
    effectivePropertyFilters,
    includeNoAssignee,
    labelFilters,
    priorityFilters,
    scope,
    showSubIssues,
    sort.sort_by,
    sort.sort_direction,
    statusFilters,
    viewIncludeNoProject,
    viewProjectFilters,
    workingIssueIDs,
  ]);
  // 下面每个消费方——facet 请求、状态/分组分支 hook、还有 Table 自己的 `useQueries`
  // 列表——都以这个对象的引用身份做 key。把它钉到内容上，好让上游某个不稳定的依赖
  // 无法为一条没变的查询重建它们全部。
  const tableQuerySpec = useStableByContent(derivedTableQuerySpec);

  const [activeTableFacet, setActiveTableFacet] =
    useState<IssueTableFacetSpec | null>(null);
  const requestedFacets = useMemo<IssueTableFacetSpec[]>(() => {
    const facets: IssueTableFacetSpec[] = [];
    if (usesServerStatusSurface) facets.push({ kind: "status" });
    if (
      activeTableFacet &&
      !facets.some(
        (facet) =>
          facet.kind === activeTableFacet.kind &&
          (facet.kind !== "property" ||
            activeTableFacet.kind !== "property" ||
            facet.property_id === activeTableFacet.property_id),
      )
    ) {
      facets.push(activeTableFacet);
    }
    // 即便处于禁用态，请求结构也保持完整。
    return facets.length > 0 ? facets : [{ kind: "status" }];
  }, [activeTableFacet, usesServerStatusSurface]);
  const tableFacetRequest = useMemo(
    () => ({
      query: tableQuerySpec,
      facets: requestedFacets,
      // 状态类 surface 把 facet 总数当作权威的空态依据。表格行/分组自己已持有
      // 显示用的总数。
      include_total: usesServerStatusSurface,
    }),
    [requestedFacets, tableQuerySpec, usesServerStatusSurface],
  );
  const tableFacetsQuery = useQuery({
    ...issueTableFacetsOptions(wsId, tableFacetRequest),
    placeholderData: keepPreviousData,
    // 计数只在一个打开的筛选子菜单里可见。把每个自定义属性 facet 都急切加载，
    // 会让一次 Table 挂载发到最多 47 条 SQL，并在失效后反复扫问题表。
    enabled:
      usesServerStatusSurface ||
      ((usesTable || usesServerGroupSurface) && activeTableFacet !== null),
  });
  // 表头 chip 的计数，单独放一条查询而不是折进上面那个子菜单 facet 请求，有两个理由：
  // 那个请求刻意做成懒加载（常开式的 facet 会在 Table/分组 surface 挂载时就把它
  // 重新启用）；而且这条 spec 去掉了 `working_issue_ids`，切换筛选不会改变查询身份
  // ——点击它标注的那个 chip 时数字绝不能闪。
  const workingAgentsQuerySpec = useMemo<IssueTableQuerySpec>(() => {
    if (!agentRunningFilter) return tableQuerySpec;
    const { working_issue_ids: _working, ...filters } = tableQuerySpec.filters;
    return { ...tableQuerySpec, filters };
  }, [agentRunningFilter, tableQuerySpec]);
  const workingAgentsFacetRequest = useMemo(
    () => ({
      query: workingAgentsQuerySpec,
      facets: [{ kind: "working_agents" } as const],
      include_total: false,
    }),
    [workingAgentsQuerySpec],
  );
  const workingAgentsFacetQuery = useQuery({
    ...issueTableFacetsOptions(wsId, workingAgentsFacetRequest),
    placeholderData: keepPreviousData,
    // Gantt 用自己的计数：它的画布投影在客户端做、无法表达成 Table 查询 spec，
    // 所以 facet 答案会多算。
    //
    // 成员/智能体详情面板（actor panel）根本不渲染 agents-working chip，没有东西会读
    // 这个答案——而且没有开关可切时，`agentRunningFilter` 在那里不可达。与其每次面板
    // 挂载都付一次聚合开销，不如跳过。要是给那个头部加 chip，就得删掉这个条件。
    enabled: !usesGantt && scope.type !== "actor",
  });
  const facetWorkingAgents = useMemo<WorkingAgentSummary[] | undefined>(() => {
    const facet = workingAgentsFacetQuery.data?.facets.find(
      (candidate) => candidate.kind === "working_agents",
    );
    // 不含这个 facet 的后端（旧部署）会报错或返回省略它的响应。保持「不确定」，
    // 而不是谎称零。
    if (!facet) return undefined;
    return facet.values.map((value) => ({
      id: value.key,
      running_task_count: value.count,
    }));
  }, [workingAgentsFacetQuery.data]);
  useEffect(() => {
    if (!usesServerFacets) setActiveTableFacet(null);
  }, [usesServerFacets]);
  const requestActiveTableFacet = useCallback(
    (facet: IssueTableFacetSpec | null) => {
      setActiveTableFacet(usesServerFacets ? facet : null);
    },
    [usesServerFacets],
  );
  const serverStatusBranches = useIssueStatusBranches({
    wsId,
    query: tableQuerySpec,
    statuses: serverStatuses,
    facets: tableFacetsQuery.data,
    facetsPending: tableFacetsQuery.isPending,
    facetsFetching: tableFacetsQuery.isFetching,
    enabled: usesServerStatusSurface && !statusFilterUnresolved,
  });
  const serverGroupSpec = useMemo<IssueTableGroupsRequest["group"]>(() => {
    if (effectiveViewMode === "swimlane") {
      return {
        kind: "compound",
        primary: swimlaneGrouping,
        // 和 board/list 分支同一个上线开关：`status_category` 是本特性引入的约定，
        // 只有目录确认这个工作区确实有自定义状态后才发送——那只能在集群已跑这个版本
        // 时为真。否则泳道保留它过去发起的那个请求原样。（MUL-6243）
        secondary: hasCustomStatuses ? "status_category" : "status",
        secondary_values: serverStatuses,
      };
    }
    if (effectiveGrouping === "project") return { kind: "project" };
    const propertyId = propertyIdFromViewKey(effectiveGrouping);
    if (propertyId) {
      return {
        kind: "property",
        property_id: propertyId,
        include_empty: true,
      };
    }
    return { kind: "assignee" };
  }, [
    effectiveGrouping,
    effectiveViewMode,
    hasCustomStatuses,
    serverStatuses,
    swimlaneGrouping,
  ]);
  const serverGroupQuery = useMemo<IssueTableQuerySpec>(() => {
    if (effectiveViewMode !== "swimlane") return tableQuerySpec;
    const { statuses: _statuses, ...filters } = tableQuerySpec.filters;
    return { ...tableQuerySpec, filters };
  }, [effectiveViewMode, tableQuerySpec]);
  const serverGroupBranches = useIssueGroupBranches({
    wsId,
    query: serverGroupQuery,
    group: serverGroupSpec,
    secondaryValues:
      effectiveViewMode === "swimlane" ? serverStatuses : undefined,
    observeEmptyBranches:
      effectiveViewMode === "swimlane" ||
      (effectiveViewMode === "board" && activeGroupingProperty !== null),
    enabled: usesServerGroupSurface && !statusFilterUnresolved,
  });

  // 选中只在当前会员窗口内有意义：批量操作作用于选中的 id，而导出/公共字段消费方要拿
  // 它跟可见行求交，所以一份在会员变化后存活的选中，会让「已选 1 项」对不同消费方
  // 指不同集合（round-2 review P1#2）。凡是影响会员的输入一变就重置。排序刻意排除在外
  // ——重排不改变会员。运行中的集合也排除：agents-working 筛选开启期间，一个任务
  // 结束不应在用户操作到一半时清掉他的选中。
  const membershipKey = useMemo(
    () =>
      JSON.stringify([
        statusFilters,
        priorityFilters,
        assigneeFilters,
        includeNoAssignee,
        creatorFilters,
        viewProjectFilters,
        viewIncludeNoProject,
        labelFilters,
        effectivePropertyFilters,
        agentRunningFilter,
        showSubIssues,
        dateParams,
        debouncedActiveSearch,
      ]),
    [
      agentRunningFilter,
      assigneeFilters,
      creatorFilters,
      dateParams,
      debouncedActiveSearch,
      effectivePropertyFilters,
      includeNoAssignee,
      labelFilters,
      priorityFilters,
      showSubIssues,
      statusFilters,
      viewIncludeNoProject,
      viewProjectFilters,
    ],
  );
  const selection = useCreateIssueSurfaceSelection(
    scopeKey,
    `${scopeKey}:${effectiveViewMode}:${membershipKey}`,
  );

  const data = useIssueSurfaceData({
    wsId,
    queryPlan,
    projectId,
    usesGantt,
    usesTable,
    serverStatusBranches,
    serverGroupBranches,
    ganttShowCompleted,
    statusFilters,
    hiddenStatusCategories,
    statusFilterPending,
    statusFilterError,
    priorityFilters,
    assigneeFilters,
    includeNoAssignee,
    agentRunningFilter,
    creatorFilters,
    projectFilters: viewProjectFilters,
    includeNoProject: viewIncludeNoProject,
    labelFilters,
    propertyFilters: effectivePropertyFilters,
    workingIssueIDs,
    showSubIssues,
    loadProjects:
      cardProperties.project ||
      (usesTable && tableColumns.some((column) => column.key === "project")) ||
      // 按项目分组的分组头要通过 projects 查询解析标题，所以即使没有卡片/列显示项目
      // 本身，按项目分组也得加载它。
      (usesTable && tableGrouping === "project") ||
      (effectiveViewMode === "board" && effectiveGrouping === "project") ||
      (effectiveViewMode === "swimlane" && swimlaneGrouping === "project"),
  });

  // Gantt 画的是客户端物化出的画布，所以它的 chip 数持有那些画布行的智能体。
  // 其他每种视图模式都取服务端 facet。
  const workingAgents = useMemo<WorkingAgentSummary[] | undefined>(() => {
    if (!usesGantt) return facetWorkingAgents;
    const rows = data.ganttWorkingScopeIssues;
    if (!rows) return undefined;
    const visible = new Set(rows.map((issue) => issue.id));
    const summaries: WorkingAgentSummary[] = [];
    for (const agent of workspaceWorkingAgents) {
      const running = agent.issue_ids.filter((id) => visible.has(id)).length;
      if (running > 0) {
        summaries.push({ id: agent.id, running_task_count: running });
      }
    }
    return summaries;
  }, [
    data.ganttWorkingScopeIssues,
    facetWorkingAgents,
    usesGantt,
    workspaceWorkingAgents,
  ]);

  const exportTableIssues = useCallback(async () => {
    const issues: Issue[] = [];
    const seenIssueIds = new Set<string>();
    const seenCursors = new Set<string>();
    let fingerprint: string | null = null;
    let expectedTotal: number | null = null;
    let cursor: string | null = null;
    do {
      if (cursor !== null) {
        if (seenCursors.has(cursor)) throw new IssueTableExportIntegrityError();
        seenCursors.add(cursor);
      }
      const page = await api.listIssueTableRows({
        query: tableQuerySpec,
        group: { kind: "none" },
        group_key: null,
        hierarchy: { enabled: false },
        parent_id: null,
        page: { limit: 100, cursor },
      });
      // parseWithFallback 刻意用空响应保护交互视图免受 schema 漂移影响。导出必须反其道
      // 而行、失败即关闭：空的 fingerprint 是兜底哨兵，绝不能产出一份看着像成功的截断 CSV。
      if (!page.query_fingerprint) throw new IssueTableExportIntegrityError();
      fingerprint ??= page.query_fingerprint;
      if (cursor === null) expectedTotal = page.total;
      if (
        page.query_fingerprint !== fingerprint ||
        page.group_key !== null ||
        page.parent_id !== null
      ) {
        throw new IssueTableExportIntegrityError();
      }
      for (const row of page.rows) {
        if (seenIssueIds.has(row.issue.id)) {
          throw new IssueTableExportIntegrityError();
        }
        seenIssueIds.add(row.issue.id);
        issues.push(row.issue);
      }
      cursor = page.next_cursor;
    } while (cursor);
    if (issues.length !== (expectedTotal ?? 0)) {
      throw new IssueTableExportIntegrityError();
    }
    return issues;
  }, [tableQuerySpec]);

  const { actions, openCreateIssue, moveIssue } = useIssueSurfaceActions({
    createDefaults: resolvedCreateDefaults,
  });

  const { ganttWorkingScopeIssues: _ganttWorkingScope, ...surfaceData } = data;

  return {
    scopeKey,
    projectId,
    createDefaults: resolvedCreateDefaults,
    viewMode: effectiveViewMode,
    allowGantt: allowedModes.has("gantt") && !!projectId,
    ...surfaceData,
    workingAgents,
    hasActiveFilters,
    statusPagination: usesServerStatusSurface
      ? data.statusPagination
      : undefined,
    groupBranches: usesServerGroupSurface
      ? serverGroupBranches
      : undefined,
    // 搜索结果是空时也保持 TableView 挂载，让它的本地搜索控件继续可用来细化或清除查询。
    // 同时把防抖值也算进去，避免清掉的查询还在等重新拉取未搜索窗口时闪一下空屏。
    isEmpty:
      data.isEmpty &&
      !data.isRefreshing &&
      !(usesTable && (tableSearch.trim() || debouncedActiveSearch)),
    isStatusCatalogError: data.isStatusCatalogError,
    retryStatusCatalog: catalog.retry,
    sort,
    actions,
    selection,
    tableSearch,
    tableQuerySpec,
    tableFacetCounts:
      usesServerStatusSurface ||
      ((usesTable || usesServerGroupSurface) && activeTableFacet !== null)
        ? tableFacetsQuery.data
        : undefined,
    facetCountsExact:
      !usesTable && !usesServerStatusSurface && !usesServerGroupSurface,
    setActiveTableFacet: requestActiveTableFacet,
    setTableSearch,
    openCreateIssue,
    moveIssue,
    exportTableIssues,
  };
}
