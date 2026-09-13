"use client";

// 问题界面（IssueSurface）主组件：按 scope（工作区/我的/项目/成员/团队）渲染看板、
// 列表、表格、甘特、泳道之一，统一处理已保存视图的叠加、Provider 挂载、
// 加载/为空/出错等各状态，并按数据窗口整体重建以隔离跨窗口的缓存污染。
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { AlertTriangle, FilterX, ListTodo, Plus } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  useViewStore,
  ViewStoreProvider,
} from "@multica/core/issues/stores/view-store-context";
import {
  getIssueSurfaceViewStore,
  seedIssueSurfaceViewState,
} from "@multica/core/issues/stores/surface-view-store";
import { useActiveIssueView } from "@multica/core/issue-views/use-active-view";
import { baselineFromQuery } from "@multica/core/issue-views/baseline";
import { ViewBaselineProvider, useViewBaseline } from "./view-baseline-context";
import type { IssueViewScope } from "@multica/core/issue-views/queries";
import {
  actorKindForViewVariant,
  issueScopeKey,
  myRelationForViewVariant,
  type IssueScope,
} from "@multica/core/issues/surface/scope";
import type { Issue } from "@multica/core/types";
import { BatchActionToolbar } from "../components/batch-action-toolbar";
import { IssuesHeader } from "../components/issues-header";
import { useT } from "../../i18n";
import { IssueContextMenuProvider } from "../actions";
import { IssueSurfaceActionsProvider } from "./actions-context";
import { IssueSurfaceSelectionProvider } from "./selection-context";
import type { IssueCreateDefaults, IssueSurfaceProps } from "./types";
import {
  useIssueSurfaceController,
  type IssueSurfaceController,
} from "./use-issue-surface-controller";

const BoardView = lazy(() =>
  import("../components/board-view").then((module) => ({
    default: module.BoardView,
  })),
);
const ListView = lazy(() =>
  import("../components/list-view").then((module) => ({
    default: module.ListView,
  })),
);
const TableView = lazy(() =>
  import("../components/table-view").then((module) => ({
    default: module.TableView,
  })),
);
const GanttView = lazy(() =>
  import("../components/gantt-view").then((module) => ({
    default: module.GanttView,
  })),
);
const SwimLaneView = lazy(() =>
  import("../components/swimlane-view").then((module) => ({
    default: module.SwimLaneView,
  })),
);

/** 传给自定义 header/空态/加载态渲染函数的最小上下文。 */
export interface IssueSurfaceRenderContext {
  controller: IssueSurfaceController;
  issues: Issue[];
}

interface IssueSurfaceComponentProps extends IssueSurfaceProps {
  renderHeader?: (context: IssueSurfaceRenderContext) => ReactNode;
  renderEmpty?: (context: IssueSurfaceRenderContext) => ReactNode;
  renderLoading?: (context: IssueSurfaceRenderContext) => ReactNode;
  clientFilter?: (issue: Issue) => boolean;
  showClientEmpty?: (context: IssueSurfaceRenderContext) => boolean;
  batchToolbar?: "always" | "list" | "never";
  contentClassName?: string;
}

/**
 * 问题界面组件：按 scope 渲染问题列表/看板/表格/甘特/泳道，并串起已保存视图、
 * 各 Provider（右键菜单、写操作、多选、视图基线）与加载/为空/出错状态。
 * @param props.scope - 数据范围（工作区/我的/项目/成员/团队）。
 * @param props.modes - 允许的视图模式集合（按先后序决定回退模式）。
 * @param props.surfaceKey - 视图偏好 key；指定后各视图偏好在各自 key 下隔离持久化。
 * @param props.createDefaults - 创建问题弹窗的默认值。
 * @param props.search - 服务端持有的搜索词（非 Table 界面使用）。
 * @param props.renderHeader - 自定义头部渲染（默认用 IssuesHeader）。
 * @param props.renderEmpty - 自定义空态渲染。
 * @param props.renderLoading - 自定义加载态渲染。
 * @param props.clientFilter - 仅客户端生效的额外过滤。
 * @param props.showClientEmpty - 在 clientFilter 生效且结果为空时，是否显示「被筛空」空态。
 * @param props.batchToolbar - 批量操作栏的出现策略。
 * @param props.contentClassName - 内容区额外样式。
 */
export function IssueSurface({
  scope,
  modes,
  surfaceKey,
  createDefaults,
  search,
  renderHeader,
  renderEmpty,
  renderLoading,
  clientFilter,
  showClientEmpty,
  batchToolbar = "always",
  contentClassName,
}: IssueSurfaceComponentProps) {
  const wsId = useWorkspaceId();
  // 已保存视图只存在于工作区 / 我的 / 项目三类 surface 上。
  const viewScope: IssueViewScope | null =
    scope.type === "workspace"
      ? { scope_type: "workspace" }
      : scope.type === "my"
        ? { scope_type: "my" }
        : scope.type === "project"
          ? { scope_type: "project", scope_id: scope.projectId }
          : null;
  const { activeView } = useActiveIssueView(wsId, viewScope);

  // 打开某个已保存视图时，把 surface 切换到该视图自己的偏好 key 上：
  // 显示/额外筛选的调整会按「人 × 视图」持久化，底层的内置 surface 状态绝不触碰
  // （它没有草稿机制）。
  const resolvedSurfaceKey = activeView
    ? `view:${activeView.id}`
    : (surfaceKey ?? issueScopeKey(scope));
  const seedDefinition = activeView
    ? { ...activeView.query, ...activeView.display }
    : null;
  const viewBaseline = useMemo(
    () => (activeView ? baselineFromQuery(activeView.query) : null),
    [activeView],
  );
  // 视图打开期间，视图捕获的 scope 轴属于视图，而不属于用户正站着的标签页：
  // 工作区/项目视图带指派者类型变体，我的视图带关系变体。用户自己的标签页状态
  // 从不被改动——视图关闭（或消失）时它恰好原样留在原地。无变体的视图解析为
  // 不受限的轴值。
  const effectiveScope: IssueScope =
    activeView && (scope.type === "workspace" || scope.type === "project")
      ? { ...scope, actorKind: actorKindForViewVariant(activeView.scope_variant) }
      : activeView && scope.type === "my"
        ? { ...scope, relation: myRelationForViewVariant(activeView.scope_variant) }
        : scope;
  const store = useMemo(() => {
    // 首次打开时的种子写入发生在这里——就在该 key 的 store 被创建的时刻：
    // 那时还没有任何 React 组件订阅新 key 的 store，里面的 setState 不会级联成
    // 渲染中的更新循环（它曾经正是因为在组件体内执行才出过问题）。
    if (seedDefinition) {
      seedIssueSurfaceViewState(resolvedSurfaceKey, seedDefinition);
    }
    return getIssueSurfaceViewStore(resolvedSurfaceKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- seed identity follows the key
  }, [resolvedSurfaceKey]);

  // 每次这个 key 变化，都会拆掉并整体重建整个 surface（Provider、拖拽、所有列/卡片）——
  // 对数据窗口变化是有意为之，但贵到「意外的翻转」都是性能 bug。留一个仅开发环境的
  // 面包屑，让 Performance trace 里出现双重挂载时能追溯到具体的 key 变化。
  const contentKey = `${wsId}:${issueScopeKey(effectiveScope)}:${activeView ? `view:${activeView.id}` : "default"}`;
  useEffect(() => {
    if (process.env.NODE_ENV !== "production") {
      console.warn(`[issue-surface] mount ${contentKey}`);
    }
  }, [contentKey]);

  return (
    <ViewStoreProvider store={store}>
      <ViewBaselineProvider baseline={viewBaseline}>
      {/* 数据窗口变化时整体重建：列表查询把上一个 key 的数据保留为占位（keepPreviousData），
          所以同一个 surface 内的排序/筛选变化永远不会闪骨架屏——但如果跨窗口复用已挂载的
          observer，会出现 A 项目的卡片冒充 B 项目（且 isLoading=false、也没有骨架屏）直到
          B 的数据到达。按窗口 key 重建让新窗口拿到全新 observer：冷窗口 → 骨架屏，
          热窗口 → 立刻缓存命中。窗口身份是 wsId + scope——带上 wsId 是因为工作区布局在
          切换工作区时不会重建，而两个工作区会共享同一个 scope key（如 "workspace:all"）。
          这里按「数据身份」而非 surfaceKey（视图偏好身份）作为 key。 */}
      <IssueSurfaceContent
        key={contentKey}
        scope={effectiveScope}
        modes={modes}
        createDefaults={createDefaults}
        search={search}
        renderHeader={renderHeader}
        renderEmpty={renderEmpty}
        renderLoading={renderLoading}
        clientFilter={clientFilter}
        showClientEmpty={showClientEmpty}
        batchToolbar={batchToolbar}
        contentClassName={contentClassName}
      />
      </ViewBaselineProvider>
    </ViewStoreProvider>
  );
}

function IssueSurfaceContent({
  scope,
  modes,
  createDefaults,
  search,
  renderHeader,
  renderEmpty,
  renderLoading,
  clientFilter,
  showClientEmpty,
  batchToolbar,
  contentClassName,
}: Omit<IssueSurfaceComponentProps, "surfaceKey">) {
  const { t } = useT("projects");
  const controller = useIssueSurfaceController({
    scope,
    modes,
    createDefaults,
    search,
  });
  const [tableLoadedIssues, setTableLoadedIssues] = useState<Issue[]>([]);
  const handleTableLoadedIssuesChange = useCallback((next: Issue[]) => {
    setTableLoadedIssues((current) =>
      current.length === next.length &&
      current.every((issue, index) => issue === next[index])
        ? current
        : next,
    );
  }, []);
  useEffect(() => {
    if (controller.viewMode !== "table") setTableLoadedIssues([]);
  }, [controller.viewMode]);
  const issues = useMemo(
    () =>
      clientFilter
        ? controller.issues.filter((issue) => clientFilter(issue))
        : controller.issues,
    [clientFilter, controller.issues],
  );
  const swimlaneIssues = useMemo(
    () =>
      clientFilter
        ? controller.swimlaneIssues.filter((issue) => clientFilter(issue))
        : controller.swimlaneIssues,
    [clientFilter, controller.swimlaneIssues],
  );
  const renderContext = useMemo(
    () => ({ controller, issues }),
    [controller, issues],
  );
  const openCreateIssue = useCallback(
    (defaults?: IssueCreateDefaults) => {
      controller.openCreateIssue(defaults);
    },
    [controller],
  );
  const shouldShowClientEmpty =
    !!clientFilter &&
    issues.length === 0 &&
    (showClientEmpty ? showClientEmpty(renderContext) : true);
  const shouldShowBatchToolbar =
    batchToolbar !== "never" &&
    (batchToolbar === "always" ||
      controller.viewMode === "list" ||
      controller.viewMode === "table");

  return (
    <IssueSurfaceActionsProvider actions={controller.actions}>
      {/* 本 surface 渲染的所有卡片/行共享一个右键菜单——见 IssueContextMenuProvider。
          放在 actions provider 之内，让单例里的 useIssueActions 把更新走 surface 动作。 */}
      <IssueContextMenuProvider>
      <IssueSurfaceSelectionProvider selection={controller.selection}>
        {renderHeader ? (
          renderHeader(renderContext)
        ) : (
          <IssuesHeader
            scopedIssues={controller.surfaceIssues}
            workingAgents={controller.workingAgents}
            allowGantt={controller.allowGantt}
            isRefreshing={controller.isRefreshing}
            facetCountsExact={
              controller.facetCountsExact
            }
            tableFacetCounts={controller.tableFacetCounts}
            onTableFacetChange={controller.setActiveTableFacet}
            saveViewScope={
              scope.type === "project"
                ? { kind: "project", projectId: scope.projectId }
                : scope.type === "workspace"
                  ? { kind: "workspace" }
                  : null
            }
          />
        )}
        {/* 状态目录加载失败要先于加载/为空/内容渲染：它挂掉期间行数据的拉取是被
            挂起的（自定义状态筛选没有它就无法路由），所以下面的每个分支都会渲染出
            一个无法解释的空白 surface、且无路可走。（MUL-6243） */}
        {controller.isStatusCatalogError ? (
          <StatusCatalogErrorState onRetry={controller.retryStatusCatalog} />
        ) : controller.isLoading ? (
          renderLoading ? (
            renderLoading(renderContext)
          ) : (
            <IssueSurfaceSkeleton mode={controller.viewMode} />
          )
        ) : controller.isEmpty || shouldShowClientEmpty ? (
          // 被筛空的 surface 不等于空 surface。行数据其实存在、只是被筛选藏起来了时，
          // 声称「这里还没有问题」并推荐去创建一个是错的——而且这正是 agents 工作中的
          // chip 常把你丢进来的状态（MUL-5525）。这个分支刻意排在 `renderEmpty` 前面：
          // 各 surface 自己的空态文案描述的都是未筛选的情况。
          controller.hasActiveFilters ? (
            <FilteredEmptyState />
          ) : renderEmpty ? (
            renderEmpty(renderContext)
          ) : (
            <div className="flex flex-1 min-h-0 flex-col items-center justify-center gap-3 text-muted-foreground">
              <ListTodo className="h-10 w-10 text-faint-foreground" />
              <p className="text-body">{t(($) => $.detail.empty_issues_title)}</p>
              <p className="text-caption">{t(($) => $.detail.empty_issues_hint)}</p>
              <Button
                variant="outline"
                size="sm"
                className="mt-1"
                onClick={() => controller.openCreateIssue()}
              >
                <Plus className="size-3.5 mr-1.5" />
                {t(($) => $.detail.empty_issues_new_button)}
              </Button>
            </div>
          )
        ) : (
          <div className={cn("flex flex-col flex-1 min-h-0", contentClassName)}>
            <Suspense fallback={<IssueSurfaceSkeleton mode={controller.viewMode} />}>
              {controller.viewMode === "board" && (
              <BoardView
                issues={issues}
                visibleStatuses={controller.visibleStatuses}
                hiddenStatuses={controller.hiddenStatuses}
                onMoveIssue={controller.moveIssue}
                childProgressMap={controller.childProgressMap}
                projectMap={controller.projectMap}
                projectId={controller.projectId}
                onCreateIssue={openCreateIssue}
                statusPagination={controller.statusPagination}
                groupBranches={controller.groupBranches}
              />
              )}
              {controller.viewMode === "list" && (
              <ListView
                issues={issues}
                visibleStatuses={controller.visibleStatuses}
                childProgressMap={controller.childProgressMap}
                projectMap={controller.projectMap}
                projectId={controller.projectId}
                onMoveIssue={controller.moveIssue}
                onCreateIssue={openCreateIssue}
                statusPagination={controller.statusPagination!}
              />
              )}
              {controller.viewMode === "table" && (
              <TableView
                serverQuery={controller.tableQuerySpec}
                childProgressMap={controller.childProgressMap}
                search={controller.tableSearch}
                onSearchChange={controller.setTableSearch}
                onLoadedIssuesChange={handleTableLoadedIssuesChange}
                onCreateIssue={openCreateIssue}
                exportIssues={controller.exportTableIssues}
                resolveExportLookups={controller.resolveTableExportLookups}
              />
              )}
              {controller.viewMode === "gantt" && (
                <GanttView issues={controller.filteredGanttIssues} />
              )}
              {controller.viewMode === "swimlane" && (
              <SwimLaneView
                issues={issues}
                unfilteredIssues={swimlaneIssues}
                activeFilters={controller.activeFilters}
                visibleStatuses={controller.visibleStatuses}
                hiddenStatuses={controller.hiddenStatuses}
                onMoveIssue={controller.moveIssue}
                childProgressMap={controller.childProgressMap}
                projectMap={controller.projectMap}
                projectId={controller.projectId}
                onCreateIssue={openCreateIssue}
                groupBranches={controller.groupBranches}
              />
              )}
            </Suspense>
          </div>
        )}
        {shouldShowBatchToolbar && (
          <BatchActionToolbar
            issues={
              controller.viewMode === "table" ? tableLoadedIssues : issues
            }
          />
        )}
      </IssueSurfaceSelectionProvider>
      </IssueContextMenuProvider>
    </IssueSurfaceActionsProvider>
  );
}

/**
 * 状态目录加载失败时的错误态：带重试按钮，让被自定义状态筛选卡住的 surface 有路可走。
 */
function StatusCatalogErrorState({ onRetry }: { onRetry: () => void }) {
  const { t } = useT("issues");
  return (
    <div
      role="alert"
      className="flex flex-1 min-h-0 flex-col items-center justify-center gap-3 text-muted-foreground"
    >
      <AlertTriangle className="h-10 w-10 text-faint-foreground" />
      <p className="text-body">{t(($) => $.status_catalog_error.title)}</p>
      <p className="text-caption">{t(($) => $.status_catalog_error.hint)}</p>
      <Button variant="outline" size="sm" className="mt-1" onClick={onRetry}>
        {t(($) => $.status_catalog_error.retry)}
      </Button>
    </div>
  );
}

/**
 * surface 里有行数据、但当前激活的筛选一条都匹配不上的「被筛空」状态。
 * 所有 surface 共用它，调用方不用记得自己那份空态文案只描述了未筛选的情况。
 * 「清除筛选」按钮清除的恰是本状态所检验的那批筛选，所以点它总能恢复内容。
 */
function FilteredEmptyState() {
  const { t } = useT("issues");
  const clearFilters = useViewStore((s) => s.clearFilters);
  const resetFiltersTo = useViewStore((s) => s.resetFiltersTo);
  // 在已保存视图里「清除」会回到视图自己的条件——基线从这里不可清除（只有编辑视图
  // 的对话框才能改它）。
  const baseline = useViewBaseline();
  const handleClear = baseline
    ? () => resetFiltersTo(baseline.raw)
    : clearFilters;

  return (
    <div className="flex flex-1 min-h-0 flex-col items-center justify-center gap-3 text-muted-foreground">
      <FilterX className="h-10 w-10 text-faint-foreground" />
      <p className="text-body">{t(($) => $.filtered_empty.title)}</p>
      <p className="text-caption">{t(($) => $.filtered_empty.hint)}</p>
      <Button variant="outline" size="sm" className="mt-1" onClick={handleClear}>
        {t(($) => $.filtered_empty.clear_button)}
      </Button>
    </div>
  );
}

// 加载骨架屏刻意不包含 Table：Table 用自己的占位，画在真实网格内部的行上，
// 让表头、列宽和工具条在任何数据到达前就先立起来——surface 级替代品会用另一种
// 形状的进度条替换这一切，等行数据到达时还要再跳一次。
function IssueSurfaceSkeleton({ mode }: { mode: string }) {
  if (mode === "list") {
    return (
      <div className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto p-2">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-10 w-full rounded-lg" />
        ))}
      </div>
    );
  }

  return (
    <div className="flex flex-1 min-h-0 gap-4 overflow-x-auto p-4">
      {Array.from({ length: 5 }).map((_, i) => (
        <div key={i} className="flex min-w-52 flex-1 flex-col gap-2">
          <Skeleton className="h-4 w-20" />
          <Skeleton className="h-24 w-full rounded-lg" />
          <Skeleton className="h-24 w-full rounded-lg" />
        </div>
      ))}
    </div>
  );
}
