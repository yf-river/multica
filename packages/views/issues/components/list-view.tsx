"use client";

// 本文件：ListView —— 问题列表的"分组列表视图"。
// 按状态类别分组，每个状态一个可折叠的手风琴区块（默认展开、可收起），区块头
// 带"全选"复选框与"+ 新建"。每个展开区块内的行用 Virtuoso 虚拟化到页面共享的
// 滚动容器；行支持跨组拖拽移动（改状态）与同组内重排（仅手动排序模式）。
// 服务端按组分页，"加载更多"哨兵挂在 Virtuoso 的 Footer 上；拖拽/落定逻辑与
// board-view 共用同一套 useDragSettle，保证两个视图行为一致。

import { memo, useState, useCallback, useMemo, useEffect, useRef } from "react";
import { ChevronRight, Plus } from "lucide-react";
import { Accordion } from "@base-ui/react/accordion";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  useDroppable,
  useSensor,
  useSensors,
  type DragStartEvent,
  type DragEndEvent,
  type DragOverEvent,
} from "@dnd-kit/core";
import { SortableContext, verticalListSortingStrategy, arrayMove } from "@dnd-kit/sortable";
import { Virtuoso } from "react-virtuoso";
import { Button } from "@multica/ui/components/ui/button";
import type { Issue, IssueStatusCategory, Project } from "@multica/core/types";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import { StatusHeading } from "./status-heading";
import { ListRow, DraggableListRow, type ChildProgress } from "./list-row";
import { useDragSettle } from "./use-drag-settle";
import { ListLoadMoreFooter } from "./list-load-more-footer";
import { useT } from "../../i18n";
import {
  type DragMoveUpdates,
  makeKanbanCollision,
  statusGroupId,
  buildColumns,
  computePosition,
  findColumn,
  getMoveAnchors,
  insertIdByPosition,
  issueMatchesGroup,
  getMoveUpdates,
} from "../utils/drag-utils";
import type { BoardColumnGroup } from "./board-column";
import { useIssueSurfaceSelection } from "../surface/selection-context";
import type { IssueCreateDefaults } from "../surface/types";
import type {
  IssueStatusPageState,
  IssueStatusPagination,
} from "../surface/use-issue-status-branches";
import { VirtuosoSeed, VIRTUOSO_SEED_COUNT } from "../../common/virtuoso-seed";
import { DeferredTooltip } from "../../common/deferred-tooltip";
import { useRestoredScrollRef } from "../../platform";

// 列表行高固定 36px (h-9)。把这个估算值同时给"种子渲染区"的尾部占位与
// Virtuoso 的 defaultItemHeight 使用，共享滚动器从第一帧起高度就是真实的——
// 既避免滚动条在"种子 → Virtuoso"交接时来回重绘，也让恢复的 scrollTop
// 在 ref 附着时能直接生效 (MUL-4741)。
const LIST_ROW_ESTIMATED_HEIGHT = 36;

const EMPTY_PROGRESS_MAP = new Map<string, ChildProgress>();
const EMPTY_IDS: string[] = [];

function buildListGroups(visibleStatuses: IssueStatusCategory[]): BoardColumnGroup[] {
  return visibleStatuses.map((status) => ({
    id: statusGroupId(status),
    title: status,
    status,
    createData: { status },
  }));
}

function ListViewImpl({
  issues,
  visibleStatuses,
  childProgressMap = EMPTY_PROGRESS_MAP,
  projectMap,
  statusPagination,
  projectId,
  onMoveIssue,
  onCreateIssue,
}: {
  issues: Issue[];
  visibleStatuses: IssueStatusCategory[];
  childProgressMap?: Map<string, ChildProgress>;
  projectMap?: Map<string, Project>;
  statusPagination: IssueStatusPagination;
  projectId?: string;
  onMoveIssue?: (issueId: string, updates: DragMoveUpdates, onSettled?: () => void) => void;
  onCreateIssue?: (defaults: IssueCreateDefaults) => void;
}) {
  const listCollapsedStatuses = useViewStore(
    (s) => s.listCollapsedStatuses
  );
  const toggleListCollapsed = useViewStore(
    (s) => s.toggleListCollapsed
  );
  const sortBy = useViewStore((s) => s.sortBy);
  const { t } = useT("issues");

  const sortFieldKey = sortBy === "created_at" ? "created" : sortBy;
  const sortLabel = sortBy !== "position"
    ? t(($) => $.board.ordered_by, { field: t(($) => $.display[`sort_${sortFieldKey}` as keyof typeof $.display]) })
    : null;

  const expandedStatuses = useMemo(
    () =>
      visibleStatuses.filter(
        (s) => !listCollapsedStatuses.includes(s)
      ),
    [visibleStatuses, listCollapsedStatuses]
  );

  const dragEnabled = !!onMoveIssue;

  const groups = useMemo(
    () => buildListGroups(visibleStatuses),
    [visibleStatuses],
  );
  const groupIds = useMemo(
    () => new Set(groups.map((g) => g.id)),
    [groups],
  );
  const groupMap = useMemo(
    () => new Map(groups.map((g) => [g.id, g])),
    [groups],
  );

  // --- 拖拽状态 ---
  const [activeIssue, setActiveIssue] = useState<Issue | null>(null);
  // 共享的拖拽/落定原语（见 use-drag-settle）——与 board-view 是同一套状态机，
  // 两个视图的拖拽行为才不会各自漂移。
  const {
    columns,
    setColumns,
    columnsRef,
    isDraggingRef,
    isSettlingRef,
    recentlyMovedRef,
    settleVersion,
    beginSettle,
  } = useDragSettle(() => buildColumns(issues, groups, "status"));

  useEffect(() => {
    if (!isDraggingRef.current && !isSettlingRef.current) {
      setColumns(buildColumns(issues, groups, "status"));
    }
  }, [issues, groups, settleVersion, setColumns, isDraggingRef, isSettlingRef]);

  const issueMap = useMemo(() => {
    const map = new Map<string, Issue>();
    for (const issue of issues) map.set(issue.id, issue);
    return map;
  }, [issues]);

  const issueMapRef = useRef(issueMap);
  if (!isDraggingRef.current && !isSettlingRef.current) {
    issueMapRef.current = issueMap;
  }

  const collisionDetection = useMemo(
    () => makeKanbanCollision(groupIds),
    [groupIds],
  );

  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: { distance: 5 },
    })
  );

  const handleDragStart = useCallback(
    (event: DragStartEvent) => {
      isDraggingRef.current = true;
      const issue = issueMapRef.current.get(event.active.id as string) ?? null;
      setActiveIssue(issue);
    },
    [isDraggingRef],
  );

  const handleDragOver = useCallback(
    (event: DragOverEvent) => {
      const { active, over } = event;
      if (!over || recentlyMovedRef.current) return;

      const activeId = active.id as string;
      const overId = over.id as string;

      setColumns((prev) => {
        const activeCol = findColumn(prev, activeId, groupIds);
        const overCol = findColumn(prev, overId, groupIds);
        if (!activeCol || !overCol || activeCol === overCol) return prev;

        if (sortBy !== "position") return prev;

        recentlyMovedRef.current = true;
        const oldIds = prev[activeCol]!.filter((id) => id !== activeId);
        const newIds = [...prev[overCol]!];
        const overIndex = newIds.indexOf(overId);
        const insertIndex = overIndex >= 0 ? overIndex : newIds.length;
        newIds.splice(insertIndex, 0, activeId);
        return { ...prev, [activeCol]: oldIds, [overCol]: newIds };
      });
    },
    [groupIds, sortBy, recentlyMovedRef, setColumns],
  );

  const handleDragEnd = useCallback(
    (event: DragEndEvent) => {
      const { active, over } = event;
      isDraggingRef.current = false;
      setActiveIssue(null);

      const resetColumns = () =>
        setColumns(buildColumns(issues, groups, "status"));

      if (!over || !onMoveIssue) {
        resetColumns();
        return;
      }

      const activeId = active.id as string;
      const overId = over.id as string;

      const cols = columnsRef.current;
      const activeCol = findColumn(cols, activeId, groupIds);
      const overCol = findColumn(cols, overId, groupIds);
      if (!activeCol || !overCol) {
        resetColumns();
        return;
      }

      let finalColumns = cols;
      if (activeCol === overCol && sortBy === "position") {
        const ids = cols[activeCol]!;
        const oldIndex = ids.indexOf(activeId);
        const newIndex = ids.indexOf(overId);
        if (oldIndex !== -1 && newIndex !== -1 && oldIndex !== newIndex) {
          const reordered = arrayMove(ids, oldIndex, newIndex);
          finalColumns = { ...cols, [activeCol]: reordered };
          setColumns(finalColumns);
        }
      }

      const finalCol = sortBy === "position"
        ? findColumn(finalColumns, activeId, groupIds)
        : overCol;
      if (!finalCol) {
        resetColumns();
        return;
      }
      const finalGroup = groupMap.get(finalCol);
      if (!finalGroup) {
        resetColumns();
        return;
      }

      const map = issueMapRef.current;

      if (sortBy !== "position") {
        const currentIssue = map.get(activeId);
        if (!currentIssue || issueMatchesGroup(currentIssue, finalGroup)) {
          resetColumns();
          return;
        }
        // 立刻乐观地把行移进目标组。否则在 sortBy != "position" 的分支里，
        // 落地时本地列从未被动过，行会待在原组直到整个请求结束、mutation 落定
        // 才跳过去——正是 board-view 修过的"先弹回原列、再移动"闪烁。
        // 插入位置按缓存同款逻辑（insertIdByPosition）推算，因此落定时用缓存
        // 重建的列与当前显示一致，视觉上没有任何跳动。
        const targetIds = insertIdByPosition(
          (cols[finalCol] ?? []).filter((id) => id !== activeId),
          activeId,
          currentIssue.position,
          map,
        );
        setColumns((prev) => {
          const fromIds = (prev[activeCol] ?? []).filter((cid) => cid !== activeId);
          return { ...prev, [activeCol]: fromIds, [finalCol]: targetIds };
        });
        onMoveIssue(
          activeId,
          {
            ...getMoveUpdates(finalGroup, currentIssue.position, currentIssue),
            ...getMoveAnchors(targetIds, activeId),
          },
          beginSettle(),
        );
        return;
      }

      const finalIds = finalColumns[finalCol]!;
      const newPosition = computePosition(finalIds, activeId, map);
      const currentIssue = map.get(activeId);

      if (
        currentIssue &&
        issueMatchesGroup(currentIssue, finalGroup) &&
        currentIssue.position === newPosition
      ) {
        return;
      }

      // beginSettle() 在落定时还会递增 settleVersion（board-view 做了、而此前
      // 这个分支没有做），这样一次失败的位置移动会触发回滚，而不是让行滞留
      // 在落点位置。
      onMoveIssue(
        activeId,
        {
          ...getMoveUpdates(finalGroup, newPosition, currentIssue),
          ...getMoveAnchors(finalIds, activeId),
        },
        beginSettle(),
      );
    },
    [issues, groups, onMoveIssue, groupIds, groupMap, sortBy, beginSettle, setColumns, columnsRef, isDraggingRef],
  );

  // 一次拖拽被中止（pointercancel、窗口缩放、标签页隐藏、Escape）时，dnd-kit
  // 触发的是 onDragCancel 而非 onDragEnd。触屏浏览器几乎总会走到这条路径：
  // 从某一行上发起的滚动手势先越过 5px 的激活距离，随后浏览器接管手势去做
  // 原生滚动并取消指针。没有这个处理器，`isDraggingRef` 会在整个会话里一直为
  // true——列镜像冻结、不再跟随缓存更新；又因为手风琴的 onValueChange 由同一
  // ref 把关，点击状态表头也变成空操作，所有分组都再也无法收起 (MUL-6240)。
  const handleDragCancel = useCallback(() => {
    isDraggingRef.current = false;
    setActiveIssue(null);
    setColumns(buildColumns(issues, groups, "status"));
  }, [issues, groups, setColumns, isDraggingRef]);

  // 每个状态面板的 Virtuoso 都把同一个滚动容器当作 customScrollParent，所以用
  // 回调 ref 在容器挂载后把元素交给各面板。只保留一个滚动器（而非每个面板各
  // 一个）才能维持现有的"吸顶表头 + 跨区块滚动"行为；每个展开面板内部，被
  // 虚拟化的只是行本身。
  const [scrollEl, setScrollEl] = useState<HTMLDivElement | null>(null);
  // 拉取式的滚动恢复 (MUL-4741)：共享滚动器附着时写入保存的滚动偏移量——
  // 各状态的种子区加上估算高度的占位，让它在首次提交时就拥有真实高度可依。
  const restoreScrollRef = useRestoredScrollRef("list");
  const attachScroller = useCallback(
    (el: HTMLDivElement | null) => {
      setScrollEl(el);
      restoreScrollRef(el);
    },
    [restoreScrollRef],
  );

  const content = (
    <Accordion.Root
      multiple
      className="space-y-1"
      value={expandedStatuses}
      onValueChange={(value: string[]) => {
        if (isDraggingRef.current) return;
        for (const status of visibleStatuses) {
          const wasExpanded = expandedStatuses.includes(status);
          const isExpanded = value.includes(status);
          if (wasExpanded !== isExpanded) {
            toggleListCollapsed(status as IssueStatusCategory);
          }
        }
      }}
    >
      {visibleStatuses.map((status) => {
        const isExpanded = expandedStatuses.includes(status);
        return (
          <StatusAccordionItem
            key={status}
            status={status}
            issueIds={columns[statusGroupId(status)] ?? EMPTY_IDS}
            issueMap={issueMapRef.current}
            childProgressMap={childProgressMap}
            projectMap={projectMap}
            page={statusPagination[status]}
            projectId={projectId}
            onCreateIssue={onCreateIssue}
            dragEnabled={dragEnabled}
            isExpanded={isExpanded}
            sortLabel={sortLabel}
            scrollParent={scrollEl}
          />
        );
      })}
    </Accordion.Root>
  );

  if (!dragEnabled) {
    return (
      <div ref={attachScroller} data-tab-scroll-root="list" className="flex-1 min-h-0 overflow-y-auto p-2 pt-0">
        {content}
      </div>
    );
  }

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={collisionDetection}
      onDragStart={handleDragStart}
      onDragOver={handleDragOver}
      onDragEnd={handleDragEnd}
      onDragCancel={handleDragCancel}
    >
      <div ref={attachScroller} data-tab-scroll-root="list" className="flex-1 min-h-0 overflow-y-auto p-2 pt-0">
        {content}
      </div>

      <DragOverlay dropAnimation={null}>
        {activeIssue ? (
          <div className="max-w-2xl rotate-1 cursor-grabbing opacity-90 shadow-lg shadow-black/10 rounded-md border border-border bg-card px-4 py-2">
            <span className="text-caption text-muted-foreground mr-2">{activeIssue.identifier}</span>
            <span className="text-body">{activeIssue.title}</span>
          </div>
        ) : null}
      </DragOverlay>
    </DndContext>
  );
}

function StatusAccordionItem({
  status,
  issueIds,
  issueMap,
  childProgressMap,
  projectMap,
  page,
  projectId,
  onCreateIssue,
  dragEnabled,
  isExpanded,
  sortLabel,
  scrollParent,
}: {
  status: IssueStatusCategory;
  issueIds: string[];
  issueMap: Map<string, Issue>;
  childProgressMap: Map<string, ChildProgress>;
  projectMap?: Map<string, Project>;
  page: IssueStatusPageState;
  projectId?: string;
  onCreateIssue?: (defaults: IssueCreateDefaults) => void;
  dragEnabled: boolean;
  isExpanded: boolean;
  sortLabel: string | null;
  scrollParent: HTMLElement | null;
}) {
  const { t } = useT("issues");
  const selection = useIssueSurfaceSelection();
  const selectedIds = selection.selectedIds;
  const select = selection.select;
  const deselect = selection.deselect;

  const issues = useMemo(
    () => issueIds.flatMap((id) => {
      const issue = issueMap.get(id);
      return issue ? [issue] : [];
    }),
    [issueIds, issueMap],
  );

  const selectedCount = issueIds.filter((id) => selectedIds.has(id)).length;
  const allSelected = issues.length > 0 && selectedCount === issues.length;
  const someSelected = selectedCount > 0;

  const { setNodeRef: setDroppableRef, isOver } = useDroppable({
    id: statusGroupId(status),
    disabled: !dragEnabled,
  });

  const disableSorting = !!sortLabel;

  // 无限滚动哨兵搭乘 Virtuoso 的 Footer，因此它落在虚拟化行的真正末端，
  // 滚动到那里时 loadMore 依然能被触发。
  const listComponents = useMemo(
    () => ({
      // Footer 恒为对象、绝不传 undefined：react-virtuoso 在 `components` 是
      // undefined 时会直接抛错 (MUL-4474)。区块短小且没有分页时，Footer 自身
      // 渲染 null。
      Footer: () => (
        <ListLoadMoreFooter
          hasMore={page.hasMore}
          isLoading={page.isLoading || page.isFetching}
          total={page.total}
          onLoadMore={page.loadMore}
          isError={page.isError}
          onRetry={page.retry}
        />
      ),
    }),
    [page],
  );

  const computeItemKey = (_index: number, issue: Issue) => issue.id;
  const itemContent = (_index: number, issue: Issue) =>
    dragEnabled ? (
      <DraggableListRow
        issue={issue}
        childProgress={childProgressMap.get(issue.id)}
        project={
          issue.project_id ? projectMap?.get(issue.project_id) : undefined
        }
        disableSorting={disableSorting}
      />
    ) : (
      <ListRow
        issue={issue}
        childProgress={childProgressMap.get(issue.id)}
        project={
          issue.project_id ? projectMap?.get(issue.project_id) : undefined
        }
      />
    );

  // 行被虚拟化进页面共享的滚动父容器，且只在区块"展开且非空"时渲染——折叠
  // （0 高度 / 隐藏）面板里的 Virtuoso 没有视口可测量。当共享滚动父容器仍是
  // null（路由返回后的重挂载里回调 ref 尚未落定）时，先用种子渲染一批数量
  // 有界的真实行，避免首帧空白；容器一旦就位，就挂载 Virtuoso 并配上匹配的
  // `initialItemCount`，让测量帧保留住这些行而不是闪成空白 (MUL-4750)。
  // 无论是否虚拟化，drop 目标、SortableContext、吸顶表头与折叠行为都不变——
  // 虚拟化只决定屏幕外的行是否进入 DOM。
  const rows =
    isExpanded && issues.length > 0 ? (
      scrollParent ? (
        <Virtuoso
          customScrollParent={scrollParent}
          data={issues}
          computeItemKey={computeItemKey}
          initialItemCount={Math.min(issues.length, VIRTUOSO_SEED_COUNT)}
          defaultItemHeight={LIST_ROW_ESTIMATED_HEIGHT}
          increaseViewportBy={{ top: 400, bottom: 400 }}
          components={listComponents}
          itemContent={itemContent}
        />
      ) : (
        <VirtuosoSeed
          data={issues}
          itemContent={itemContent}
          computeItemKey={computeItemKey}
          estimatedItemHeight={LIST_ROW_ESTIMATED_HEIGHT}
        />
      )
    ) : null;

  return (
    <Accordion.Item value={status} ref={dragEnabled ? setDroppableRef : undefined}>
      <Accordion.Header
        className={`group/header sticky top-0 z-10 flex h-10 items-center rounded-lg bg-muted transition-colors hover:bg-accent ${
          isOver && !isExpanded
            ? "ring-2 ring-brand/25 bg-accent/15"
            : ""
        }`}
      >
        <div className="pl-3 flex items-center">
          <input
            type="checkbox"
            checked={allSelected}
            ref={(el) => {
              if (el) el.indeterminate = someSelected && !allSelected;
            }}
            onChange={() => {
              if (allSelected) {
                deselect(issueIds);
              } else {
                select(issueIds);
              }
            }}
            className="cursor-pointer accent-primary"
          />
        </div>
        <Accordion.Trigger className="group/trigger flex flex-1 items-center gap-2 px-2 h-full text-left outline-none cursor-pointer">
          <ChevronRight className="size-3.5 shrink-0 text-muted-foreground transition-transform group-aria-expanded/trigger:rotate-90" />
          <StatusHeading status={status} count={page.total} />
        </Accordion.Trigger>
        {onCreateIssue && (
          <div className="pr-2">
            {/* 懒挂载的 tooltip 机制 —— 见 DeferredTooltip。 */}
            <DeferredTooltip
              content={t(($) => $.list.add_issue_tooltip)}
              trigger={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="rounded-full text-muted-foreground opacity-0 group-hover/header:opacity-100 transition-opacity"
                  onClick={() => {
                    const defaults = {
                      status,
                      ...(projectId ? { project_id: projectId } : {}),
                    };
                    onCreateIssue(defaults);
                  }}
                >
                  <Plus className="size-3.5" />
                </Button>
              }
            />
          </div>
        )}
      </Accordion.Header>
      <Accordion.Panel>
        {issues.length > 0 ? (
          dragEnabled ? (
            <SortableContext items={issueIds} strategy={verticalListSortingStrategy}>
              {rows}
            </SortableContext>
          ) : (
            rows
          )
        ) : (
          <p className="py-6 text-center text-caption text-muted-foreground">
            {t(($) => $.list.empty_status)}
          </p>
        )}
      </Accordion.Panel>
    </Accordion.Item>
  );
}

/**
 * 分组列表视图（套了 memo）。为什么 memo：surface 控制器会在加载标志翻转时
 * （例如视图切换使某个查询转为启用）重渲染——没有 memo，每次这样的翻转都会
 * 重渲染整棵视图树（数百毫秒）。传入的全部 props 都是控制器产出的、引用稳定
 * 的 useMemo / useCallback 结果，因此 memo 能安全短路。
 */
export const ListView = memo(ListViewImpl);
