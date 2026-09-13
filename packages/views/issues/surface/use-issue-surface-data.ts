"use client";

// 问题界面的数据层 hook：按视图模式取数并统一过滤——状态/分组分支（服务端数据）或
// 客户端窗口；再派生出各视图需要的 issues、泳道、甘特行、可见/隐藏列、子进度表等。
import { useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Issue, Project } from "@multica/core/types";
import { ALL_STATUSES } from "@multica/core/issues/config";
import { projectListOptions } from "@multica/core/projects/queries";
import { childIssueProgressOptions } from "@multica/core/issues/queries";
import { issueSurfaceGanttOptions } from "@multica/core/issues/surface/repository";
import type { IssueSurfaceQueryPlan } from "@multica/core/issues/surface/query-plan";
import type { IssueStatus, IssueStatusCategory, PropertyFilterValue } from "@multica/core/types";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { issueBehavesAsAny, statusFilterColumns } from "@multica/core/issues";
import {
  applyIssueFilters,
  type IssueFilterState,
  type IssueFilters,
} from "../utils/filter";
import type { ChildProgress } from "../components/list-row";
import type {
  IssueStatusBranches,
  IssueStatusPagination,
} from "./use-issue-status-branches";
import type { IssueGroupBranches } from "./use-issue-group-branches";

const EMPTY_ISSUES: Issue[] = [];
const EMPTY_CHILD_PROGRESS = new Map<string, ChildProgress>();
const EMPTY_PROJECTS: Project[] = [];

/**
 * 甘特画布真正绘制的行：在共享筛选之上，还要满足甘特自己的两条规则——一行得有日期
 * 才能被摆放，以及除非用户要求否则隐藏已完成工作。数据源只交付已排期的问题
 * （服务端 `scheduled=true`），但一行仍可能没带日期到达——比如刚被 WS 驱动的乐观补丁
 * 清掉 start_date / due_date、正在等缓存重拉——所以日期检查保持防御性。
 *
 * 这两条规则放在这里而不是 GanttView 内部私藏，是为了让表头 chip 能收敛到和画布相同的
 * 集合。一个视图自己偷偷过滤行，正是 chip 计数当初和列表漂移的根源（MUL-4884）；
 * 在两个地方重复这套规则只是换个姿势重新引入漂移。
 */
function ganttCanvasRows(issues: Issue[], showCompleted: boolean): Issue[] {
  const dated = issues.filter((i) => i.start_date || i.due_date);
  if (showCompleted) return dated;
  // 按「分类」判断：处于 done/cancelled 分类的自定义状态同样是已完成工作，
  // 「显示已完成」也得把它藏起来。（MUL-6243）
  return dated.filter((i) => !issueBehavesAsAny(i, ["done", "cancelled"]));
}

/** useIssueSurfaceData 返回的问题界面数据。 */
export interface IssueSurfaceData {
  surfaceIssues: Issue[];
  projectIssues: Issue[];
  issues: Issue[];
  swimlaneIssues: Issue[];
  /** 仅甘特：agents-working 筛选会留在屏幕上的画布行。其他视图模式为 `undefined`，
   *  它们的表头 chip 从服务端 `working_agents` facet 取计数。 */
  ganttWorkingScopeIssues: Issue[] | undefined;
  filteredGanttIssues: Issue[];
  ganttIssues: Issue[];
  visibleStatuses: IssueStatusCategory[];
  hiddenStatuses: IssueStatusCategory[];
  statusPagination: IssueStatusPagination;
  activeFilters: Omit<IssueFilters, "statusFilters">;
  childProgressMap: Map<string, ChildProgress>;
  projectMap: Map<string, Project>;
  resolveTableExportLookups: (needs: {
    projects: boolean;
    childProgress: boolean;
  }) => Promise<{
    projectMap: Map<string, Project>;
    childProgressMap: Map<string, ChildProgress>;
  }>;
  isLoading: boolean;
  /**
   * 自定义状态筛选所依赖的目录请求失败。没有它筛选无法兑现，
   * 所以 surface 显示一个可重试的错误，而不是一个无法解释的空看板。（MUL-6243）
   */
  isStatusCatalogError: boolean;
  /** 当前窗口的数据正在被重校验，同时把上一次的快照作为占位展示（排序/日期变化，
   *  或任何分组看板的筛选变化）。驱动表头的延迟刷新指示——内容保持在原地，
   *  所以这不是一个加载态。 */
  isRefreshing: boolean;
  isEmpty: boolean;
}

/**
 * 问题界面的数据 hook：根据视图模式把「原始数据 + 全部筛选」变换成各视图要消费的
 * 派生数据（issues、泳道、甘特行、可见/隐藏列、进度/项目表等）。
 * 服务端分支数据（状态/分组）直接透传，客户端窗口数据在此统一套用筛选。
 */
export function useIssueSurfaceData({
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
  projectFilters,
  includeNoProject,
  labelFilters,
  propertyFilters,
  workingIssueIDs,
  showSubIssues,
  loadProjects,
}: {
  wsId: string;
  queryPlan: IssueSurfaceQueryPlan;
  projectId?: string;
  usesGantt: boolean;
  usesTable: boolean;
  serverStatusBranches: IssueStatusBranches;
  serverGroupBranches: IssueGroupBranches;
  /** 甘特的「显示已完成」显示开关。没有它画布会隐藏 done/cancelled 行，
   *  所以「工作中范围」也得遵守它。 */
  ganttShowCompleted: boolean;
  statusFilters: IssueStatus[];
  hiddenStatusCategories: IssueStatusCategory[];
  /** 自定义状态筛选正在等目录——保持加载态。 */
  statusFilterPending: boolean;
  /** 目录失败，自定义状态筛选无法兑现。 */
  statusFilterError: boolean;
  priorityFilters: IssueFilterState["priorityFilters"];
  assigneeFilters: IssueFilterState["assigneeFilters"];
  includeNoAssignee: boolean;
  agentRunningFilter: boolean;
  creatorFilters: IssueFilterState["creatorFilters"];
  projectFilters: string[];
  includeNoProject: boolean;
  labelFilters: string[];
  propertyFilters: Record<string, PropertyFilterValue[]>;
  /** 由 `/api/working-agents` 投影出的、有任务在跑的问题 id 集合。 */
  workingIssueIDs: ReadonlySet<string>;
  showSubIssues: boolean;
  loadProjects: boolean;
}): IssueSurfaceData {
  const ganttIssuesQuery = useQuery({
    ...issueSurfaceGanttOptions(wsId, projectId ?? "", queryPlan),
    enabled: usesGantt,
  });
  const workingFilterContext = useMemo(
    () => ({ runningIssueIds: workingIssueIDs }),
    [workingIssueIDs],
  );
  const bucketedIssues = serverStatusBranches.enabled
    ? serverStatusBranches.issues
    : serverGroupBranches.enabled
      ? serverGroupBranches.issues
      : EMPTY_ISSUES;

  // `cancelled` 是一等公民的默认状态（MUL-4290）：它和别的状态一样被拉进缓存、
  // 直接流经 list / board / swimlane 列、表头 facet 计数、批量选中和 isEmpty 判断。
  // 状态筛选像筛其他状态一样筛它——它不再解锁一个本被藏起来的分桶。
  const ganttIssues = ganttIssuesQuery.data ?? EMPTY_ISSUES;
  const surfaceIssues = usesGantt
    ? ganttIssues
    : usesTable
      ? EMPTY_ISSUES
      : bucketedIssues;

  const baseFilterState = useMemo<IssueFilterState>(
    () => ({
      statusFilters,
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      projectFilters,
      includeNoProject,
      labelFilters,
      propertyFilters,
      workingOnly: agentRunningFilter,
      showSubIssues,
    }),
    [
      assigneeFilters,
      agentRunningFilter,
      creatorFilters,
      includeNoAssignee,
      includeNoProject,
      labelFilters,
      priorityFilters,
      projectFilters,
      propertyFilters,
      showSubIssues,
      statusFilters,
    ],
  );

  const issues = useMemo(
    () =>
      serverStatusBranches.enabled
        ? surfaceIssues
        : applyIssueFilters(
            surfaceIssues,
            baseFilterState,
            workingFilterContext,
          ),
    [
      baseFilterState,
      serverStatusBranches.enabled,
      surfaceIssues,
      workingFilterContext,
    ],
  );

  const statuslessFilterState = useMemo<IssueFilterState>(
    () => ({
      ...baseFilterState,
      statusFilters: [],
    }),
    [baseFilterState],
  );

  const swimlaneIssues = useMemo(
    () =>
      applyIssueFilters(
        surfaceIssues,
        statuslessFilterState,
        workingFilterContext,
      ),
    [statuslessFilterState, surfaceIssues, workingFilterContext],
  );

  const filteredGanttIssues = useMemo(
    () =>
      ganttCanvasRows(
        applyIssueFilters(ganttIssues, baseFilterState, workingFilterContext),
        ganttShowCompleted,
      ),
    [
      baseFilterState,
      ganttIssues,
      ganttShowCompleted,
      workingFilterContext,
    ],
  );

  const workingFilterState = useMemo<IssueFilterState>(
    () => ({
      ...baseFilterState,
      workingOnly: true,
    }),
    [baseFilterState],
  );

  // 甘特里 agents-working 筛选会留在屏幕上的画布行——也就是你在甘特里点表头 chip
  // 时看到的那一批。
  //
  // 甘特是唯一一个会员不由服务端持有的 surface：它从一份完全物化的已排期问题窗口
  // 里画，而它的画布投影（scheduled + dated + showCompleted）无法表达成 Table 查询
  // spec。所以表头 chip 在这里不能从服务端 `working_agents` facet 取计数，只能从这
  // 个集合推导。
  //
  // 其他每种视图模式（list / board / swimlane / table）都通过那个 facet 解析同一
  // 个问题——针对与行同源的 scope + 筛选。为了装饰 chip 而在客户端重建一整份问题窗口，
  // 正是服务端 facet 存在要避免的事。
  const ganttWorkingScopeIssues = useMemo(() => {
    if (!usesGantt) return undefined;
    return ganttCanvasRows(
      applyIssueFilters(ganttIssues, workingFilterState, workingFilterContext),
      ganttShowCompleted,
    );
  }, [
    ganttIssues,
    ganttShowCompleted,
    usesGantt,
    workingFilterState,
    workingFilterContext,
  ]);

  const {
    data: childProgressData,
    refetch: refetchChildProgress,
  } = useQuery(childIssueProgressOptions(wsId));
  const childProgressMap = childProgressData ?? EMPTY_CHILD_PROGRESS;
  const {
    data: projectData,
    refetch: refetchProjects,
  } = useQuery({
    ...projectListOptions(wsId),
    enabled: loadProjects,
  });
  const projects = projectData ?? EMPTY_PROJECTS;
  const projectMap = useMemo(
    () => new Map(projects.map((project) => [project.id, project])),
    [projects],
  );
  const resolveTableExportLookups = useCallback(
    async (needs: { projects: boolean; childProgress: boolean }) => {
      const [projectResult, progressResult] = await Promise.all([
        needs.projects ? refetchProjects() : Promise.resolve(null),
        needs.childProgress
          ? refetchChildProgress()
          : Promise.resolve(null),
      ]);
      if (projectResult?.error) throw projectResult.error;
      if (progressResult?.error) throw progressResult.error;
      if (needs.projects && !projectResult?.data) {
        throw new Error("Failed to load project data for export");
      }
      if (needs.childProgress && !progressResult?.data) {
        throw new Error("Failed to load child progress for export");
      }
      const resolvedProjects = projectResult?.data ?? projects;
      return {
        projectMap: new Map(
          resolvedProjects.map((project) => [project.id, project]),
        ),
        childProgressMap: progressResult?.data ?? childProgressMap,
      };
    },
    [
      childProgressMap,
      projects,
      refetchChildProgress,
      refetchProjects,
    ],
  );

  const catalog = useIssueStatuses(wsId);

  const visibleStatuses = useMemo<IssueStatusCategory[]>(() => {
    // 看板列是「分类」，不是状态 key——加一个自定义状态绝不能加一列。有两条独立的东西
    // 会收窄它们：隐藏列（显示状态），以及以具体 KEY 表达、因此必须映射回那些 KEY 所
    // 落列的状态筛选。默认视图展示全部分类，`cancelled` 排最后（它在 ALL_STATUSES
    // 里的规范位置）。（MUL-6243）
    const resolved =
      statusFilters.length > 0 ? statusFilterColumns(statusFilters, catalog) : null;
    // pending/error 在这里不产生任何收窄；surface 的加载与错误态
    // （statusFilterPending / statusFilterError）才是拦住它把空结果当真去渲染的东西。
    const selected = resolved?.state === "resolved" ? resolved.columns : null;
    return ALL_STATUSES.filter(
      (s) =>
        !hiddenStatusCategories.includes(s) &&
        (selected === null || selected.has(s)),
    );
  }, [statusFilters, hiddenStatusCategories, catalog]);

  // 隐藏列是当前不可见的生命周期状态，所以 `cancelled` 跟其余状态一样参与看板的
  // 显示/隐藏控制。
  const hiddenStatuses = useMemo<IssueStatusCategory[]>(
    () => ALL_STATUSES.filter((s) => !visibleStatuses.includes(s)),
    [visibleStatuses],
  );

  const activeFilters = useMemo(
    () => ({
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      agentRunningFilter,
      runningIssueIds: workingIssueIDs,
      creatorFilters,
      projectFilters,
      includeNoProject,
      labelFilters,
      propertyFilters,
      showSubIssues,
    }),
    [
      assigneeFilters,
      agentRunningFilter,
      creatorFilters,
      includeNoAssignee,
      includeNoProject,
      labelFilters,
      propertyFilters,
      priorityFilters,
      projectFilters,
      showSubIssues,
      workingIssueIDs,
    ],
  );

  // `statusFilterPending` 让 surface 在自定义状态筛选等着目录说清它属于哪列时保持加载。
  // 没有它，surface 会报告「已加载、零结果」——整个冷加载窗口里一片空看板、连 spinner
  // 都没有。（MUL-6243）
  const isLoading =
    statusFilterPending ||
    (serverGroupBranches.enabled
      ? serverGroupBranches.isLoading
      : usesGantt
        ? ganttIssuesQuery.isLoading
        : serverStatusBranches.enabled
          ? serverStatusBranches.isLoading
          : false);

  // 只对「当前激活的查询」做占位式重校验。首次加载走 isLoading（没有旧数据可占位）；
  // 甘特没有占位阶段（它的 key 不携带排序/筛选）。
  const isRefreshing = serverGroupBranches.enabled
    ? serverGroupBranches.isRefreshing
    : serverStatusBranches.enabled
      ? serverStatusBranches.isRefreshing
      : false;

  return {
    surfaceIssues,
    projectIssues: surfaceIssues,
    issues,
    swimlaneIssues,
    ganttWorkingScopeIssues,
    filteredGanttIssues,
    ganttIssues,
    visibleStatuses,
    hiddenStatuses,
    statusPagination: serverStatusBranches.pagination,
    activeFilters,
    childProgressMap,
    projectMap,
    resolveTableExportLookups,
    isLoading,
    isRefreshing,
    // isEmpty 断言「这个窗口里没有问题」。board/list/swimlane 的数据就是完整窗口，
    // 所以空结果足以证明。甘特查询只是已排期问题的投影——空子集证明不了窗口为空，
    // 所以绝不下这个断言（和「不确定 → 不下断言」的会员规则同源）。GanttView 渲染自己
    // 准确的「没有已排期问题」空态，而不是通用的创建问题空态。Table 自己管理分层的
    // 加载/空/重试状态，所以这份共享的旧 surface 投影绝不断言 Table 为空。
    isEmpty:
      !isLoading &&
      !statusFilterError &&
      !usesGantt &&
      !usesTable &&
      (serverStatusBranches.enabled
        ? serverStatusBranches.isTotalKnown &&
          serverStatusBranches.total === 0
        : serverGroupBranches.enabled &&
          !serverGroupBranches.isError &&
          serverGroupBranches.total === 0),
    isStatusCatalogError: statusFilterError,
  };
}
