"use client";

// 本文件：BoardView —— 问题列表的"看板（纵列分组）视图"。
// 列可按状态 / 负责人 / 项目 / 自定义 select 属性分组；卡片可跨列拖拽移动（改
// 所属分组字段）与同列内拖拽排序（仅在手动排序下）。拖动期间本地列镜像由
// useDragSettle 锁住、防缓存刷新重建；属性分组列的移动走属性 mutation，其余走
// 通用 onMoveIssue。支持用左键拖动空白处横向平移画布（#6700），以及隐藏列面板、
// 服务端分组列的无限滚动。整体 memo 化以扛住 surface 控制器的加载态重渲染。

import { useState, useCallback, useMemo, useEffect, useRef, memo } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  useSensor,
  useSensors,
  type DragStartEvent,
  type DragEndEvent,
  type DragOverEvent,
} from "@dnd-kit/core";
import { arrayMove } from "@dnd-kit/sortable";
import { toast } from "sonner";
import type {
  Issue,
  IssueAssigneeType,
  IssueStatusCategory,
  Project,
  IssueProperty,
} from "@multica/core/types";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import { propertyIdFromViewKey } from "@multica/core/issues/stores/view-store";
import { propertyListOptions, useSetIssueProperty, useUnsetIssueProperty } from "@multica/core/properties";
import { useWorkspaceId } from "@multica/core/hooks";
import type { IssueGrouping } from "@multica/core/issues/stores/view-store";
import { useActorName } from "@multica/core/workspace/hooks";
import { BoardColumn, BOARD_CARD_WIDTH, type BoardColumnGroup } from "./board-column";
import { BoardCardContent } from "./board-card";
import { HiddenColumnsPanel, HiddenColumnRow } from "./hidden-columns-panel";
import { InfiniteScrollSentinel } from "./infinite-scroll-sentinel";
import { ListLoadMoreFooter } from "./list-load-more-footer";
import type { ChildProgress } from "./list-row";
import type { IssueCreateDefaults } from "../surface/types";
import type {
  IssueStatusPageState,
  IssueStatusPagination,
} from "../surface/use-issue-status-branches";
import type {
  IssueGroupBranches,
  IssueGroupPageState,
} from "../surface/use-issue-group-branches";
import { useDragSettle } from "./use-drag-settle";
import { useBoardDragPan } from "./use-board-drag-pan";
import { useT } from "../../i18n";
import {
  type DragMoveUpdates,
  makeKanbanCollision,
  statusGroupId,
  assigneeGroupId,
  buildColumns,
  computePosition,
  findColumn,
  getMoveAnchors,
  insertIdByPosition,
  issueMatchesGroup,
  getMoveUpdates,
  propertyGroupId,
  projectGroupId,
} from "../utils/drag-utils";

function isStatusGroup(
  group: BoardColumnGroup,
): group is BoardColumnGroup & { status: IssueStatusCategory } {
  return group.status !== undefined;
}

interface ProjectColumnLabels {
  noProject: string;
  /** 项目查询解析不到的项目 id——已删除或对该成员不可见。与 Table 共用措辞，
   *  让同一项目的看板列与表格分组行永不说出两种说法。 */
  unavailableProject: string;
}

interface BuildGroupsContext extends ProjectColumnLabels {
  getActorName: (type: string, id: string) => string;
  groupingProperty: IssueProperty | null;
  projectMap: Map<string, Project> | undefined;
  noAssigneeLabel: string;
  noValueLabel: string;
}

/**
 * 一个项目列。客户端回退（从已加载卡片派生）与服务端路径（从分组描述符派生）
 * 共用本函数，保证两者对同一项目永不说出两种描述。
 */
function projectColumn(
  id: string,
  projectId: string | null,
  projectMap: Map<string, Project> | undefined,
  labels: ProjectColumnLabels,
  totalCount?: number,
): BoardColumnGroup {
  const project = projectId ? projectMap?.get(projectId) ?? null : null;
  return {
    id,
    title: projectId
      ? project?.title ?? labels.unavailableProject
      : labels.noProject,
    projectId,
    project,
    totalCount,
    createData: { project_id: projectId },
  };
}

/**
 * 让"无项目"列始终作为 drop 目标存在——即便工作区里每张卡片当前都挂着项目，拖拽清空
 * 某卡片的项目也必须可行。完全没有列的空看板则原样放过：那是该 surface 的空态，
 * 不是"少了一列的看板"。
 */
function withNoProjectColumn(
  columns: BoardColumnGroup[],
  projectMap: Map<string, Project> | undefined,
  labels: ProjectColumnLabels,
): BoardColumnGroup[] {
  if (columns.length === 0) return columns;
  if (columns.some((column) => column.projectId === null)) return columns;
  // "无项目"在服务端排序第一，所以它一旦存在必在描述符第一页——
  // 不会出现"这一页没有，后面某页才补上"的情况。
  return [
    projectColumn(projectGroupId(null), null, projectMap, labels, 0),
    ...columns,
  ];
}

function buildGroups(
  issues: Issue[],
  visibleStatuses: IssueStatusCategory[],
  grouping: IssueGrouping,
  {
    getActorName,
    groupingProperty,
    projectMap,
    noAssigneeLabel,
    noValueLabel,
    ...projectLabels
  }: BuildGroupsContext,
): BoardColumnGroup[] {
  if (grouping === "status") {
    return visibleStatuses.map((status) => ({
      id: statusGroupId(status),
      title: status,
      status,
      createData: { status },
    }));
  }

  // select 属性看板：每个选项一列（按定义顺序），末尾跟一列"无值"。空列保持可见——
  // 它们是"把值赋给卡片"的 drop 目标。
  if (groupingProperty) {
    const columns: BoardColumnGroup[] = (groupingProperty.config.options ?? []).map(
      (option) => ({
        id: propertyGroupId(groupingProperty.id, option.id),
        title: option.name,
        propertyId: groupingProperty.id,
        propertyOptionId: option.id,
        propertyOptionColor: option.color,
      }),
    );
    columns.push({
      id: propertyGroupId(groupingProperty.id, null),
      title: noValueLabel,
      propertyId: groupingProperty.id,
      propertyOptionId: null,
    });
    return columns;
  }

  // 项目看板：已加载卡片引用到的每个项目一列，外加"无项目"列。排序镜像服务端的
  // 组顺序（无项目在前，然后按项目名），让客户端回退与分页的服务端列不可能不一致。
  if (grouping === "project") {
    const columns = new Map<string, BoardColumnGroup>();
    for (const issue of issues) {
      const projectId = issue.project_id ?? null;
      const id = projectGroupId(projectId);
      if (columns.has(id)) continue;
      columns.set(id, projectColumn(id, projectId, projectMap, projectLabels));
    }
    const ordered = Array.from(columns.values()).toSorted((a, b) => {
      if (a.projectId === null) return b.projectId === null ? 0 : -1;
      if (b.projectId === null) return 1;
      return a.title.localeCompare(b.title);
    });
    return withNoProjectColumn(ordered, projectMap, projectLabels);
  }

  const groups = new Map<string, BoardColumnGroup>();
  for (const issue of issues) {
    const id = assigneeGroupId(issue.assignee_type, issue.assignee_id);
    if (groups.has(id)) continue;

    if (issue.assignee_type && issue.assignee_id) {
      groups.set(id, {
        id,
        title: getActorName(issue.assignee_type, issue.assignee_id),
        assigneeType: issue.assignee_type,
        assigneeId: issue.assignee_id,
        createData: {
          assignee_type: issue.assignee_type,
          assignee_id: issue.assignee_id,
        },
      });
      continue;
    }

    groups.set(id, {
      id,
      title: noAssigneeLabel,
      assigneeType: null,
      assigneeId: null,
      createData: {
        assignee_type: null,
        assignee_id: null,
      },
    });
  }

  const order: Record<string, number> = {
    member: 0,
    agent: 1,
    squad: 2,
    none: 3,
  };

  return Array.from(groups.values()).toSorted((a, b) => {
    const aOrder = order[a.assigneeType ?? "none"] ?? 99;
    const bOrder = order[b.assigneeType ?? "none"] ?? 99;
    if (aOrder !== bOrder) return aOrder - bOrder;
    return a.title.localeCompare(b.title);
  });
}

const EMPTY_PROGRESS_MAP = new Map<string, ChildProgress>();
const EMPTY_IDS: string[] = [];

function BoardViewImpl({
  issues,
  visibleStatuses,
  hiddenStatuses,
  onMoveIssue,
  childProgressMap = EMPTY_PROGRESS_MAP,
  projectMap,
  projectId,
  onCreateIssue,
  statusPagination,
  groupBranches,
}: {
  issues: Issue[];
  visibleStatuses: IssueStatusCategory[];
  hiddenStatuses: IssueStatusCategory[];
  onMoveIssue: (issueId: string, updates: DragMoveUpdates, onSettled?: () => void) => void;
  childProgressMap?: Map<string, ChildProgress>;
  projectMap?: Map<string, Project>;
  /** 设置时，每列的"+"会在新建表单里预填项目。 */
  projectId?: string;
  onCreateIssue?: (defaults: IssueCreateDefaults) => void;
  statusPagination?: IssueStatusPagination;
  groupBranches?: IssueGroupBranches;
}) {
  const { t } = useT("issues");
  const storeGrouping = useViewStore((s) => s.grouping);
  const sortBy = useViewStore((s) => s.sortBy);
  const boardWsId = useWorkspaceId();
  const { data: workspaceProperties = [] } = useQuery(propertyListOptions(boardWsId));
  const groupingPropertyId = propertyIdFromViewKey(storeGrouping);
  const groupingProperty = groupingPropertyId
    ? workspaceProperties.find((p) => p.id === groupingPropertyId && p.type === "select") ?? null
    : null;
  // 持久化的 `property:<id>` 分组，若其属性定义已消失（归档/删除/来自其他工作区），
  // 回退到状态列。
  const grouping: IssueGrouping =
    groupingPropertyId && !groupingProperty ? "status" : storeGrouping;
  const groupingOptionIds = useMemo(
    () =>
      groupingProperty
        ? new Set((groupingProperty.config.options ?? []).map((option) => option.id))
        : undefined,
    [groupingProperty],
  );
  const setIssuePropertyMutation = useSetIssueProperty();
  const unsetIssuePropertyMutation = useUnsetIssueProperty();
  const applyPropertyGroupValue = useCallback(
    (group: BoardColumnGroup, issueId: string) => {
      if (group.propertyId === undefined) return;
      // 像状态/负责人拖拽那样把失败浮出来（use-issue-surface-actions）：mutation
      // 会把卡片滚回去，但没有 toast，回弹看起来就像 UI 故障而非一次被拒绝的写入。
      const onError = (err: unknown) => {
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.page.move_failed),
        );
      };
      if (group.propertyOptionId === null) {
        unsetIssuePropertyMutation.mutate(
          { issueId, propertyId: group.propertyId },
          { onError },
        );
      } else if (group.propertyOptionId !== undefined) {
        setIssuePropertyMutation.mutate(
          {
            issueId,
            propertyId: group.propertyId,
            value: group.propertyOptionId,
          },
          { onError },
        );
      }
    },
    [setIssuePropertyMutation, t, unsetIssuePropertyMutation],
  );
  const sortFieldKey = sortBy === "created_at" ? "created" : sortBy;
  const sortPropertyId = propertyIdFromViewKey(sortBy);
  const sortLabel = sortBy !== "position"
    ? t(($) => $.board.ordered_by, {
        field: sortPropertyId
          ? workspaceProperties.find((p) => p.id === sortPropertyId)?.name ?? ""
          : t(($) => $.display[`sort_${sortFieldKey}` as keyof typeof $.display]),
      })
    : null;
  const { getActorName } = useActorName();
  const groupedIssues = useMemo(
    () => (groupBranches?.enabled ? groupBranches.issues : issues),
    [groupBranches, issues],
  );
  const hydratedAssigneeGroups = useMemo<BoardColumnGroup[] | undefined>(() => {
    if (grouping === "assignee" && groupBranches?.enabled) {
      return groupBranches.descriptors.flatMap((descriptor): BoardColumnGroup[] => {
        if (descriptor.value.kind !== "assignee") return [];
        const actorRef = descriptor.value.actor;
        const actor: { type: IssueAssigneeType; id: string } | null =
          actorRef &&
          (actorRef.type === "member" ||
            actorRef.type === "agent" ||
            actorRef.type === "squad")
            ? { type: actorRef.type, id: actorRef.id }
            : null;
        return [{
          id: descriptor.key,
          title: actor
            ? getActorName(actor.type, actor.id)
            : t(($) => $.filters.no_assignee),
          assigneeType: actor?.type ?? null,
          assigneeId: actor?.id ?? null,
          totalCount: descriptor.count,
          createData: {
            assignee_type: actor?.type ?? null,
            assignee_id: actor?.id ?? null,
          },
        }];
      });
    }
    return undefined;
  }, [getActorName, groupBranches, grouping, t]);
  const projectColumnLabels = useMemo<ProjectColumnLabels>(
    () => ({
      noProject: t(($) => $.swimlane.no_project),
      unavailableProject: t(($) => $.table.value_unavailable),
    }),
    [t],
  );
  const hydratedProjectGroups = useMemo<BoardColumnGroup[] | undefined>(() => {
    if (grouping !== "project" || !groupBranches?.enabled) return undefined;
    const columns = groupBranches.descriptors.flatMap(
      (descriptor): BoardColumnGroup[] =>
        descriptor.value.kind === "project"
          ? [
              projectColumn(
                // 用服务端分组描述符自带的 key 当列 id，而不是自己另拼一个：
                // `groupPagination`（分页状态）正是按这个 key 对齐的；
                // 而 `projectGroupId` 生成的 id 又和它逐字符一致——
                // 卡片能落进这些列，靠的就是这条"同构"关系。
                descriptor.key,
                descriptor.value.project_id ?? null,
                projectMap,
                projectColumnLabels,
                descriptor.count,
              ),
            ]
          : [],
    );
    return withNoProjectColumn(columns, projectMap, projectColumnLabels);
  }, [groupBranches, grouping, projectColumnLabels, projectMap]);
  const groupPagination = useMemo(() => {
    if (!groupBranches?.enabled) return undefined;
    const grouped = new Map<string, IssueGroupPageState[]>();
    for (const descriptor of groupBranches.descriptors) {
      const page = groupBranches.pagination[descriptor.key];
      if (!page) continue;
      let id = descriptor.key;
      if (descriptor.value.kind === "property") {
        const value = descriptor.value;
        id =
          value.value_state === "value" && typeof value.value === "string"
            ? propertyGroupId(value.property_id, value.value)
            : propertyGroupId(value.property_id, null);
      }
      const pages = grouped.get(id) ?? [];
      pages.push(page);
      grouped.set(id, pages);
    }
    return Object.fromEntries(
      Array.from(grouped, ([id, pages]) => [
        id,
        {
          total: pages.reduce((sum, page) => sum + page.total, 0),
          loaded: pages.reduce((sum, page) => sum + page.loaded, 0),
          hasMore: pages.some((page) => page.hasMore),
          isLoading: pages.some((page) => page.isLoading),
          isFetching: pages.some((page) => page.isFetching),
          isError: pages.some((page) => page.isError),
          loadMore: () => {
            for (const page of pages) {
              if (page.hasMore) page.loadMore();
            }
          },
          retry: () => {
            for (const page of pages) {
              if (page.isError) page.retry();
            }
          },
        },
      ]),
    ) as Record<string, IssueGroupPageState>;
  }, [groupBranches]);
  const groups = useMemo(
    () => {
      const built =
        hydratedAssigneeGroups ??
        hydratedProjectGroups ??
        buildGroups(issues, visibleStatuses, grouping, {
          getActorName,
          groupingProperty,
          projectMap,
          noAssigneeLabel: t(($) => $.filters.no_assignee),
          noValueLabel: t(($) => $.board.no_value),
          ...projectColumnLabels,
        });
      return built.map((group) => ({
        ...group,
        totalCount: groupPagination?.[group.id]?.total ?? group.totalCount,
      }));
    },
    [hydratedAssigneeGroups, hydratedProjectGroups, issues, visibleStatuses, grouping, getActorName, groupingProperty, projectMap, projectColumnLabels, groupPagination, t],
  );
  const groupIds = useMemo(
    () => new Set(groups.map((group) => group.id)),
    [groups],
  );
  const groupMap = useMemo(
    () => new Map(groups.map((group) => [group.id, group])),
    [groups],
  );
  const collisionDetection = useMemo(
    () => makeKanbanCollision(groupIds),
    [groupIds],
  );

  // --- 拖拽状态 ---
  const [activeIssue, setActiveIssue] = useState<Issue | null>(null);
  // 共享的拖拽/落定原语（list-view、swimlane 与看板共用同一份实现，
  // 三个视图的拖拽行为才不会互相漂移）。它统一负责：本地列镜像、
  // "拖拽中/落定中"两把锁、移动结束后动画帧内的合并节流，以及落定回调。
  // 两次拖拽之间，本地列由下方的 resync effect 跟随 TanStack Query 缓存；
  // 拖拽/落定期间则被锁冻结、不再跟随。
  const {
    columns,
    setColumns,
    columnsRef,
    isDraggingRef,
    isSettlingRef,
    recentlyMovedRef,
    settleVersion,
    beginSettle,
  } = useDragSettle(() => buildColumns(groupedIssues, groups, grouping, groupingOptionIds));

  useEffect(() => {
    if (!isDraggingRef.current && !isSettlingRef.current) {
      setColumns(buildColumns(groupedIssues, groups, grouping, groupingOptionIds));
    }
  }, [groupedIssues, groups, grouping, groupingOptionIds, settleVersion, setColumns, isDraggingRef, isSettlingRef]);

  // --- issue 映射 ---
  // 拖拽期间冻结这份映射：即使 TanStack Query 恰好在拖到一半时刷新，
  // 传给 BoardColumn / DraggableBoardCard 的 props 引用也能保持稳定。
  const issueMap = useMemo(() => {
    const map = new Map<string, Issue>();
    for (const issue of groupedIssues) map.set(issue.id, issue);
    return map;
  }, [groupedIssues]);

  const issueMapRef = useRef(issueMap);
  if (!isDraggingRef.current && !isSettlingRef.current) {
    issueMapRef.current = issueMap;
  }

  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: { distance: 5 },
    })
  );

  // #6700：按住鼠标左键拖动看板的空白背景，横向平移整块画布（Trello/Linear
  // 的交互）。从卡片（`[data-board-card]`）上发起的拖拽是卡片拖拽，不受此影响。
  const pan = useBoardDragPan<HTMLDivElement>();

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
        setColumns(buildColumns(groupedIssues, groups, grouping, groupingOptionIds));

      if (!over) {
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

      // 同列内重排（仅在手动排序模式下生效）
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
        // 跨列移动：只更新所属分组字段（状态/负责人），保留卡片在组内的原有顺序。
        const currentIssue = map.get(activeId);
        if (!currentIssue || issueMatchesGroup(currentIssue, finalGroup)) {
          resetColumns();
          return;
        }
        // 立刻乐观地把卡片移进目标列。否则在 sortBy != "position" 的分支里，
        // onDragOver 全程不碰本地列，卡片会停在原列直到 mutation 落定才跳过去——
        // 正是"先弹回原列、再移动"的闪烁根因。插入位置按缓存同款逻辑
        // （insertByPosition）推算，落定时 TanStack Query 用缓存重建的列与
        // 当前显示完全一致，视觉上没有任何跳动。
        const targetIds = insertIdByPosition(
          (cols[overCol] ?? []).filter((id) => id !== activeId),
          activeId,
          currentIssue.position,
          map,
        );
        setColumns((prev) => {
          const fromIds = (prev[activeCol] ?? []).filter((cid) => cid !== activeId);
          return { ...prev, [activeCol]: fromIds, [overCol]: targetIds };
        });
        onMoveIssue(
          activeId,
          {
            ...getMoveUpdates(finalGroup, currentIssue.position, currentIssue),
            ...getMoveAnchors(targetIds, activeId),
          },
          beginSettle(),
        );
        applyPropertyGroupValue(finalGroup, activeId);
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

      // beginSettle() 先持有落定锁，并返回一个 onSettled 回调：它负责释放锁、
      // 再用缓存重建本地列——成功时是空操作（onSuccess 已把移动的卡片就地补丁
      // 到位），失败时则是回滚（onError 已恢复移动前的快照）。没有它，一次失败的
      // 移动会让卡片滞留目标列，因为 onSettled 已不再负责重新拉取。
      onMoveIssue(
        activeId,
        {
          ...getMoveUpdates(finalGroup, newPosition, currentIssue),
          ...getMoveAnchors(finalIds, activeId),
        },
        beginSettle(),
      );
      applyPropertyGroupValue(finalGroup, activeId);
    },
    [groupedIssues, groups, grouping, groupingOptionIds, onMoveIssue, groupIds, groupMap, sortBy, beginSettle, columnsRef, isDraggingRef, setColumns, applyPropertyGroupValue],
  );

  // 一次拖拽被中止（pointercancel、窗口缩放、标签页隐藏、Escape）时，dnd-kit
  // 触发的是 onDragCancel 而非 onDragEnd。在这里释放拖拽锁，列镜像才能在事后
  // 继续与缓存同步——list-view 里同样位置也有一个处理器，触屏路径正是靠它
  // 兜底才成为常规路径的 (MUL-6240)。
  const handleDragCancel = useCallback(() => {
    isDraggingRef.current = false;
    setActiveIssue(null);
    setColumns(buildColumns(groupedIssues, groups, grouping, groupingOptionIds));
  }, [groupedIssues, groups, grouping, groupingOptionIds, setColumns, isDraggingRef]);

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={collisionDetection}
      onDragStart={handleDragStart}
      onDragOver={handleDragOver}
      onDragEnd={handleDragEnd}
      onDragCancel={handleDragCancel}
    >
      <div
        ref={pan.ref}
        onPointerDown={pan.onPointerDown}
        onPointerMove={pan.onPointerMove}
        onPointerUp={pan.onPointerUp}
        onPointerCancel={pan.onPointerCancel}
        onLostPointerCapture={pan.onLostPointerCapture}
        className="flex flex-1 min-h-0 gap-4 overflow-x-auto p-2"
      >
        {groups.length === 0 ? (
          groupBranches?.isError ? (
            <button
              type="button"
              className="flex min-w-full flex-1 items-center justify-center text-body text-destructive hover:underline"
              onClick={groupBranches.retryGroups}
            >
              {t(($) => $.table.load_more_failed_retry)}
            </button>
          ) : (
            <div className="flex min-w-full flex-1 items-center justify-center text-body text-muted-foreground">
              {t(($) => $.board.empty_grouping)}
            </div>
          )
        ) : (
          groups.map((group) =>
            isStatusGroup(group) ? (
              <ServerPaginatedBoardColumn
                key={group.id}
                group={group}
                issueIds={columns[group.id] ?? EMPTY_IDS}
                issueMap={issueMapRef.current}
                childProgressMap={childProgressMap}
                projectMap={projectMap}
                page={statusPagination?.[group.status]}
                projectId={projectId}
                onCreateIssue={onCreateIssue}
                sortLabel={sortLabel}
              />
            ) : (
              groupPagination?.[group.id] ? (
                <ServerPaginatedBoardColumn
                  key={group.id}
                  group={group}
                  issueIds={columns[group.id] ?? EMPTY_IDS}
                  issueMap={issueMapRef.current}
                  childProgressMap={childProgressMap}
                  projectMap={projectMap}
                  page={groupPagination[group.id]!}
                  projectId={projectId}
                  onCreateIssue={onCreateIssue}
                  sortLabel={sortLabel}
                />
              ) : (
                <BoardColumn
                  key={group.id}
                  group={group}
                  issueIds={columns[group.id] ?? EMPTY_IDS}
                  issueMap={issueMapRef.current}
                  childProgressMap={childProgressMap}
                  projectMap={projectMap}
                  projectId={projectId}
                  onCreateIssue={onCreateIssue}
                  totalCount={group.totalCount}
                  sortLabel={sortLabel}
                />
              )
            ),
          )
        )}
        {groupBranches?.hasMoreGroups && (
          <div className="flex w-8 shrink-0 items-center justify-center">
            <InfiniteScrollSentinel
              onVisible={groupBranches.loadMoreGroups}
              loading={groupBranches.isLoadingMoreGroups}
            />
          </div>
        )}


        {grouping === "status" && hiddenStatuses.length > 0 && (
          <BoardHiddenColumnsPanel
            hiddenStatuses={hiddenStatuses}
            statusPagination={statusPagination}
          />
        )}
      </div>

      <DragOverlay dropAnimation={null}>
        {activeIssue ? (
          <div style={{ width: BOARD_CARD_WIDTH }} className="rotate-1 cursor-grabbing opacity-90 shadow-lg shadow-black/10">
            <BoardCardContent
              issue={activeIssue}
              childProgress={childProgressMap.get(activeIssue.id)}
              project={
                activeIssue.project_id
                  ? projectMap?.get(activeIssue.project_id)
                  : undefined
              }
            />
          </div>
        ) : null}
      </DragOverlay>
    </DndContext>
  );
}


const ServerPaginatedBoardColumn = memo(function ServerPaginatedBoardColumn({
  group,
  issueIds,
  issueMap,
  childProgressMap,
  projectMap,
  page,
  projectId,
  onCreateIssue,
  sortLabel,
}: {
  group: BoardColumnGroup;
  issueIds: string[];
  issueMap: Map<string, Issue>;
  childProgressMap?: Map<string, ChildProgress>;
  projectMap?: Map<string, Project>;
  page?: IssueStatusPageState | IssueGroupPageState;
  projectId?: string;
  onCreateIssue?: (defaults: IssueCreateDefaults) => void;
  sortLabel?: string | null;
}) {
  const footer = page ? (
    <ListLoadMoreFooter
      hasMore={page.hasMore}
      isLoading={page.isLoading || page.isFetching}
      total={page.total}
      onLoadMore={page.loadMore}
      isError={page.isError}
      onRetry={page.retry}
    />
  ) : undefined;
  return (
    <BoardColumn
      group={group}
      issueIds={issueIds}
      issueMap={issueMap}
      childProgressMap={childProgressMap}
      projectMap={projectMap}
      totalCount={page?.total ?? group.totalCount}
      projectId={projectId}
      onCreateIssue={onCreateIssue}
      sortLabel={sortLabel}
      footer={footer}
    />
  );
});


function BoardHiddenColumnsPanel({
  hiddenStatuses,
  statusPagination,
}: {
  hiddenStatuses: IssueStatusCategory[];
  statusPagination?: IssueStatusPagination;
}) {
  return (
    <HiddenColumnsPanel
      hiddenStatuses={hiddenStatuses}
      renderRow={(status) => (
        <HiddenColumnRow
          key={status}
          status={status}
          total={statusPagination?.[status]?.total}
        />
      )}
    />
  );
}

/**
 * 看板视图（套了 memo）。为什么 memo：surface 控制器会在加载标志翻转时
 * （例如视图切换使某个查询转为启用）重渲染——没有 memo，每次这样的翻转
 * 都会重渲染整棵看板视图树（数百毫秒）。这里传入的全部 props 都是控制器
 * 产出的、引用稳定的 useMemo / useCallback 结果，因此 memo 能安全短路。
 */
export const BoardView = memo(BoardViewImpl);
