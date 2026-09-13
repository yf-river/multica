"use client";

// 本文件：SwimLaneView —— 问题列表的"泳道（横向分组）视图"。
// 每条泳道 = 分组头 + 一行按状态列的卡片格（每格镜像一张 BoardColumn）。
// 支持按父任务 / 项目 / 负责人 / 服务端分组切换，行与卡片皆可拖拽（行=重排顺序，
// 卡片=跨泳道/跨状态移动，落地前先做本地乐观更新、mutation 落定后再与缓存对账）。
// 状态列是"类别"（自定义状态归入其行为对应的列）；泳道行由单一 Virtuoso 虚拟化，
// 只挂载可视泳道。折叠/隐藏状态等偏好经 view store 按分组持久化。

import { issueColumnCategory, issueStatusCategory, statusCategoryOfKey } from "@multica/core/issues";
import { memo, useState, useCallback, useMemo, useEffect, useRef } from "react";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  useSensor,
  useSensors,
  useDroppable,
  pointerWithin,
  closestCenter,
  type CollisionDetection,
  type DragStartEvent,
  type DragEndEvent,
  type DragOverEvent,
} from "@dnd-kit/core";
import { SortableContext, useSortable, verticalListSortingStrategy, arrayMove } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { Virtuoso } from "react-virtuoso";
import { ChevronRight, EyeOff, GripVertical, MoreHorizontal, Pencil, Plus } from "lucide-react";
import { useQuery, useQueries, useQueryClient } from "@tanstack/react-query";
import type {
  Issue,
  IssueAssigneeType,
  IssueStatus,
  IssueStatusCategory,
  IssueTableGroupDescriptor,
  Project,
  UpdateIssueRequest,
} from "@multica/core/types";
import { useViewStore, useViewStoreApi } from "@multica/core/issues/stores/view-store-context";
import { useViewBaseline } from "../surface/view-baseline-context";
import { filterIssues, type IssueFilters } from "../utils/filter";
import { getMoveAnchors } from "../utils/drag-utils";
import type { SwimlaneGrouping } from "@multica/core/issues/stores/view-store";
import { useWorkspacePaths } from "@multica/core/paths";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { useActorName } from "@multica/core/workspace/hooks";
import { childrenByParentsOptions, issueKeys } from "@multica/core/issues/queries";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@multica/ui/components/ui/dropdown-menu";
import { sortIssues } from "../utils/sort";
import { ALL_STATUSES, STATUS_CONFIG } from "@multica/core/issues/config";
import { DraggableBoardCard, BoardCardContent } from "./board-card";
import { StatusIcon } from "./status-icon";
import { Button } from "@multica/ui/components/ui/button";
import { StatusHeading } from "./status-heading";
import { HiddenColumnsPanel, HiddenColumnRow } from "./hidden-columns-panel";
import { InfiniteScrollSentinel } from "./infinite-scroll-sentinel";
import { ListLoadMoreFooter } from "./list-load-more-footer";
import { AppLink } from "../../navigation";
import { ProjectIcon } from "../../projects/components/project-icon";
import { ActorAvatar } from "../../common/actor-avatar";
import { VirtuosoSeed } from "../../common/virtuoso-seed";

import { DeferredPopup } from "../../common/deferred-popup";
import { useRestoredScrollOffset, useRestoredScrollRef } from "../../platform";
import { DeferredTooltip } from "../../common/deferred-tooltip";
import type { ChildProgress } from "./list-row";
import { useT } from "../../i18n";
import type { IssueCreateDefaults } from "../surface/types";
import type {
  IssueGroupBranches,
  IssueGroupPageState,
} from "../surface/use-issue-group-branches";

const COLUMN_WIDTH = 280;
const COLUMN_GAP = 16;

// 一条泳道行（组头 + 一排卡片格）约 300px+ 高——一个视口大约装 3 条。通用的
// VIRTUOSO_SEED_COUNT（30，按 36px 的列表行估的）会让每次 surface 重挂载都同步挂
// 起最多 30 条完整泳道（每条 = 状态列 × 卡片格 × 卡片）；6 覆盖一个视口还留了余量。
const SWIMLANE_LANE_SEED_COUNT = 6;

// 从 SwimLaneView 里提升到模块级，保证引用跨渲染稳定——useQueries 的 combine 选项
// 会经 replaceEqualDeep 用到它，但函数稳定就能省掉每次渲染的引用检查。
function combineChildrenLists(
  results: { data: Issue[] | undefined }[],
): (Issue[] | undefined)[] {
  return results.map((r) => r.data);
}

type SwimLaneMoveTargetUpdates = Pick<
  UpdateIssueRequest,
  | "parent_issue_id"
  | "project_id"
  | "assignee_type"
  | "assignee_id"
  | "status"
  | "position"
>;

type SwimLaneMoveUpdates = SwimLaneMoveTargetUpdates & {
  before_id: string | null;
  after_id: string | null;
};

function makeSwimLaneCollision(cellIds: Set<string>): CollisionDetection {
  return (args) => {
    const activeId = args.active.id as string;
    const isLaneDrag = activeId.startsWith("lane:");

    const pointer = pointerWithin(args);
    if (pointer.length > 0) {
      let filtered = pointer;
      if (isLaneDrag) {
        // 拖的是泳道：只考虑其他泳道头
        filtered = pointer.filter((c) => (c.id as string).startsWith("lane:"));
      } else {
        // 拖的是卡片：完全忽略父级泳道头
        filtered = pointer.filter((c) => !(c.id as string).startsWith("lane:"));
      }

      if (filtered.length > 0) {
        const cards = filtered.filter((c) => !cellIds.has(c.id as string));
        if (cards.length > 0) return cards;
        return filtered;
      }
    }

    const closest = closestCenter(args);
    let filteredClosest = closest;
    if (isLaneDrag) {
      filteredClosest = closest.filter((c) => (c.id as string).startsWith("lane:"));
    } else {
      filteredClosest = closest.filter((c) => !(c.id as string).startsWith("lane:"));
    }

    return filteredClosest;
  };
}

function parseCellId(id: string): { laneKey: string; status: string } | null {
  if (!id.startsWith("swim:")) return null;
  const rest = id.slice(5);
  const lastColon = rest.lastIndexOf(":");
  if (lastColon === -1) return null;
  return {
    laneKey: rest.slice(0, lastColon),
    status: rest.slice(lastColon + 1),
  };
}

function findCellIn(
  data: Record<string, Record<string, string[]>>,
  cellIds: Set<string>,
  id: string,
): { laneKey: string; status: string } | null {
  if (cellIds.has(id)) return parseCellId(id);
  for (const [pk, statusMap] of Object.entries(data)) {
    for (const [status, ids] of Object.entries(statusMap)) {
      if (ids.includes(id)) return { laneKey: pk, status };
    }
  }
  return null;
}

function cellId(laneKey: string, status: IssueStatus): string {
  return `swim:${laneKey}:${status}`;
}

const LANE_ID_PREFIX = "lane:";

/** 固定"无 X"泳道的哨兵 id 片段（分组前缀之后的部分）。 */
const NONE_LANE_ID = "none";

/** 父级分组"孤儿回退泳道"的哨兵 id 片段。 */
const ORPHAN_LANE_ID = "__orphans__";

/**
 * 可拖拽泳道头的 sortable id。固定泳道（无-X）与孤儿回退泳道使用稳定、但
 * 在同分组内唯一 的 id——避免两者都为空时 dnd-kit 悄悄把它们折叠到真实泳道 id 上。
 */
function laneIdFor(grouping: SwimlaneGrouping, rawId: string): string {
  return `${LANE_ID_PREFIX}${grouping}:${rawId}`;
}

function parseLaneId(id: string): { grouping: string; rawId: string } | null {
  if (!id.startsWith(LANE_ID_PREFIX)) return null;
  const rest = id.slice(LANE_ID_PREFIX.length);
  const firstColon = rest.indexOf(":");
  if (firstColon === -1) return null;
  return {
    grouping: rest.slice(0, firstColon),
    rawId: rest.slice(firstColon + 1),
  };
}

function computePosition(ids: string[], activeId: string, issueMap: Map<string, Issue>): number {
  const idx = ids.indexOf(activeId);
  if (idx === -1) return 0;
  const getPos = (id: string) => issueMap.get(id)?.position ?? 0;
  if (ids.length === 1) return issueMap.get(activeId)?.position ?? 0;
  if (idx === 0) return getPos(ids[1]!) - 1;
  if (idx === ids.length - 1) return getPos(ids[idx - 1]!) + 1;
  return (getPos(ids[idx - 1]!) + getPos(ids[idx + 1]!)) / 2;
}

/**
 * 一条泳道行。泳道由"按分组定制的构建器"（`buildParentLanes` / `buildProjectLanes` /
 * `buildAssigneeLanes`）产出，再被渲染器统一消费。matcher / move-updates 闭包把
 * 分组细节藏在同一个接口后面，这样 drag-end 处理器就不必按分组分支。
 */
interface LaneGroup {
  /** 完整泳道键——`<grouping>:<rawId>`。用于卡片格 id 与字典键。 */
  key: string;
  /** 在同分组内唯一的 id 片段（分组前缀之后的部分）。 */
  rawId: string;
  /** 固定泳道（无-X）。永远置顶渲染，不可拖拽。 */
  isPinned: boolean;
  /**
   * 父级分组专用的"仅展示"回退桶——规范父级不在已加载集合里的子任务。
   * 进/出的拖放都会被拒绝，因为我们无法合成缺失的父级 id。
   */
  isOrphan: boolean;
  title: string;
  identifier: string;
  /** 父任务（仅父级分组）——驱动表头里的"打开父任务"链接与状态图标。 */
  parentIssue: Pick<Issue, "id" | "status"> | null;
  /** 项目元数据（仅项目分组）——驱动表头里的图标。 */
  project: Project | null;
  /** 负责人（仅负责人分组）——驱动表头里的头像。 */
  actor: { type: IssueAssigneeType; id: string } | null;
  /** 该 issue 是否属于这条泳道。 */
  matches: (issue: Issue) => boolean;
  /**
   * issue 被放入这条泳道时交给 `onMoveIssue` 的负载片段。
   * 状态与 position 由 drag-end 处理器补齐。
   */
  moveUpdates: SwimLaneMoveTargetUpdates;
  /** 精确的服务端计数；旧式构建器不填，沿用之前"以已加载单元格窗口为准"的做法。 */
  total?: number;
  /** 按状态映射的"复合行键"（不透明）。 */
  serverCellKeys?: Partial<Record<IssueStatus, string>>;
}

const EMPTY_PROGRESS_MAP = new Map<string, ChildProgress>();
// 非父级分组用的稳定引用——没有表头时保住 `statusTotals` / `cells` 两个 memo
// 不因每次渲染而失效。
const EMPTY_HEADER_IDS = new Set<string>();
const EMPTY_PROJECTS: Project[] = [];

/**
 * 构建"父级分组"的泳道。"无父任务"泳道永远固定置顶；"其他父任务"回退桶
 *（父级未加载的子任务）在出现时追加其后。
 */
function buildParentLanes(
  visibleIssues: Issue[],
  metadataIssues: Issue[],
  storedOrder: string[],
  labels: { noParent: string; otherParents: string },
): LaneGroup[] {
  const metadataMap = new Map<string, Issue>();
  for (const issue of metadataIssues) metadataMap.set(issue.id, issue);

  const seen = new Map<string, LaneGroup>();
  let hasOrphan = false;
  for (const issue of visibleIssues) {
    if (issue.parent_issue_id === null) continue;
    const parent = metadataMap.get(issue.parent_issue_id);
    if (!parent) {
      hasOrphan = true;
      continue;
    }
    const key = `parent:${issue.parent_issue_id}`;
    if (!seen.has(key)) {
      const parentId = issue.parent_issue_id;
      seen.set(key, {
        key,
        rawId: parentId,
        isPinned: false,
        isOrphan: false,
        title: parent.title,
        identifier: parent.identifier,
        parentIssue: parent,
        project: null,
        actor: null,
        matches: (i) => i.parent_issue_id === parentId,
        moveUpdates: { parent_issue_id: parentId },
      });
    }
  }

  const orderIndex = new Map<string, number>();
  storedOrder.forEach((parentId, idx) => orderIndex.set(`parent:${parentId}`, idx));
  const ordered = Array.from(seen.values()).sort((a, b) => {
    const ai = orderIndex.get(a.key);
    const bi = orderIndex.get(b.key);
    if (ai !== undefined && bi !== undefined) return ai - bi;
    if (ai !== undefined) return -1;
    if (bi !== undefined) return 1;
    return 0;
  });

  const lanes: LaneGroup[] = [
    {
      key: `parent:${NONE_LANE_ID}`,
      rawId: NONE_LANE_ID,
      isPinned: true,
      isOrphan: false,
      title: labels.noParent,
      identifier: "",
      parentIssue: null,
      project: null,
      actor: null,
      matches: (i) => i.parent_issue_id === null,
      moveUpdates: { parent_issue_id: null },
    },
  ];
  if (hasOrphan) {
    lanes.push({
      key: `parent:${ORPHAN_LANE_ID}`,
      rawId: ORPHAN_LANE_ID,
      isPinned: true,
      isOrphan: true,
      title: labels.otherParents,
      identifier: "",
      parentIssue: null,
      project: null,
      actor: null,
      // 与规范的筛选逻辑一致：父级"不是当前渲染里某个表头泳道"的子任务。
      matches: () => false,
      moveUpdates: {},
    });
  }
  lanes.push(...ordered);
  return lanes;
}

function buildProjectLanes(
  visibleIssues: Issue[],
  projects: Project[],
  storedOrder: string[],
  labels: { noProject: string },
): LaneGroup[] {
  const projectMap = new Map<string, Project>();
  for (const p of projects) projectMap.set(p.id, p);

  const seen = new Map<string, LaneGroup>();
  for (const issue of visibleIssues) {
    if (issue.project_id === null) continue;
    const key = `project:${issue.project_id}`;
    if (seen.has(key)) continue;
    const project = projectMap.get(issue.project_id) ?? null;
    const projectId = issue.project_id;
    seen.set(key, {
      key,
      rawId: projectId,
      isPinned: false,
      isOrphan: false,
      title: project?.title ?? "",
      identifier: "",
      parentIssue: null,
      project,
      actor: null,
      matches: (i) => i.project_id === projectId,
      moveUpdates: { project_id: projectId },
    });
  }

  const orderIndex = new Map<string, number>();
  storedOrder.forEach((id, idx) => orderIndex.set(`project:${id}`, idx));
  const ordered = Array.from(seen.values()).sort((a, b) => {
    const ai = orderIndex.get(a.key);
    const bi = orderIndex.get(b.key);
    if (ai !== undefined && bi !== undefined) return ai - bi;
    if (ai !== undefined) return -1;
    if (bi !== undefined) return 1;
    return a.title.localeCompare(b.title);
  });

  return [
    {
      key: `project:${NONE_LANE_ID}`,
      rawId: NONE_LANE_ID,
      isPinned: true,
      isOrphan: false,
      title: labels.noProject,
      identifier: "",
      parentIssue: null,
      project: null,
      actor: null,
      matches: (i) => i.project_id === null,
      moveUpdates: { project_id: null },
    },
    ...ordered,
  ];
}

function buildAssigneeLanes(
  visibleIssues: Issue[],
  getActorName: (type: string, id: string) => string,
  storedOrder: string[],
  labels: { noAssignee: string },
): LaneGroup[] {
  const seen = new Map<string, LaneGroup>();
  for (const issue of visibleIssues) {
    if (issue.assignee_type === null || issue.assignee_id === null) continue;
    const assigneeType: IssueAssigneeType = issue.assignee_type;
    const assigneeId = issue.assignee_id;
    const rawId = `${assigneeType}:${assigneeId}`;
    const key = `assignee:${rawId}`;
    if (seen.has(key)) continue;
    seen.set(key, {
      key,
      rawId,
      isPinned: false,
      isOrphan: false,
      title: getActorName(assigneeType, assigneeId),
      identifier: "",
      parentIssue: null,
      project: null,
      actor: { type: assigneeType, id: assigneeId },
      matches: (i) =>
        i.assignee_type === assigneeType && i.assignee_id === assigneeId,
      moveUpdates: {
        assignee_type: assigneeType,
        assignee_id: assigneeId,
      },
    });
  }

  // 先按 actor 类型（成员在 agent 与 squad 之前）再按名字排序。
  const typeOrder: Record<string, number> = { member: 0, agent: 1, squad: 2 };
  const orderIndex = new Map<string, number>();
  storedOrder.forEach((id, idx) => orderIndex.set(`assignee:${id}`, idx));
  const ordered = Array.from(seen.values()).sort((a, b) => {
    const ai = orderIndex.get(a.key);
    const bi = orderIndex.get(b.key);
    if (ai !== undefined && bi !== undefined) return ai - bi;
    if (ai !== undefined) return -1;
    if (bi !== undefined) return 1;
    const at = typeOrder[a.actor?.type ?? ""] ?? 99;
    const bt = typeOrder[b.actor?.type ?? ""] ?? 99;
    if (at !== bt) return at - bt;
    return a.title.localeCompare(b.title);
  });

  return [
    {
      key: `assignee:${NONE_LANE_ID}`,
      rawId: NONE_LANE_ID,
      isPinned: true,
      isOrphan: false,
      title: labels.noAssignee,
      identifier: "",
      parentIssue: null,
      project: null,
      actor: null,
      matches: (i) => i.assignee_id === null,
      moveUpdates: { assignee_type: null, assignee_id: null },
    },
    ...ordered,
  ];
}

function buildServerLanes(
  descriptors: readonly IssueTableGroupDescriptor[],
  grouping: SwimlaneGrouping,
  visibleStatuses: readonly IssueStatus[],
  projects: ReadonlyMap<string, Project> | undefined,
  getActorName: (type: string, id: string) => string,
  storedOrder: string[],
  labels: {
    noParent: string;
    otherParents: string;
    noProject: string;
    noAssignee: string;
  },
): LaneGroup[] {
  const visibleStatusSet = new Set(visibleStatuses);
  const lanes = descriptors.flatMap((descriptor): LaneGroup[] => {
    if (
      (descriptor.secondary_groups ?? []).every(
        (secondary) =>
          secondary.value.kind !== "status" ||
          !visibleStatusSet.has(secondary.value.status as IssueStatus) ||
          secondary.count === 0,
      )
    ) {
      return [];
    }
    const serverCellKeys = Object.fromEntries(
      (descriptor.secondary_groups ?? []).flatMap((secondary) =>
        secondary.value.kind === "status"
          ? [[secondary.value.status, secondary.key]]
          : [],
      ),
    ) as Partial<Record<IssueStatus, string>>;
    const value = descriptor.value;
    if (grouping === "assignee" && value.kind === "assignee") {
      const actorRef = value.actor;
      const actor: { type: IssueAssigneeType; id: string } | null =
        actorRef &&
        (actorRef.type === "member" ||
          actorRef.type === "agent" ||
          actorRef.type === "squad")
          ? { type: actorRef.type, id: actorRef.id }
          : null;
      const rawId = actor ? `${actor.type}:${actor.id}` : NONE_LANE_ID;
      return [{
        key: `assignee:${rawId}`,
        rawId,
        isPinned: actor === null,
        isOrphan: false,
        title: actor
          ? getActorName(actor.type, actor.id)
          : labels.noAssignee,
        identifier: "",
        parentIssue: null,
        project: null,
        actor,
        matches: (issue) =>
          actor
            ? issue.assignee_type === actor.type &&
              issue.assignee_id === actor.id
            : issue.assignee_type === null && issue.assignee_id === null,
        moveUpdates: actor
          ? { assignee_type: actor.type, assignee_id: actor.id }
          : { assignee_type: null, assignee_id: null },
        total: descriptor.count,
        serverCellKeys,
      }];
    }
    if (grouping === "project" && value.kind === "project") {
      const rawId = value.project_id ?? NONE_LANE_ID;
      const project = value.project_id
        ? projects?.get(value.project_id) ?? null
        : null;
      return [{
        key: `project:${rawId}`,
        rawId,
        isPinned: value.project_id === null,
        isOrphan: false,
        title: value.project_id ? project?.title ?? "" : labels.noProject,
        identifier: "",
        parentIssue: null,
        project,
        actor: null,
        matches: (issue) => issue.project_id === value.project_id,
        moveUpdates: { project_id: value.project_id },
        total: descriptor.count,
        serverCellKeys,
      }];
    }
    if (grouping === "parent" && value.kind === "parent") {
      const rawId = value.parent_id ?? NONE_LANE_ID;
      const unavailable = value.value_state === "unavailable";
      return [{
        key: `parent:${rawId}`,
        rawId,
        isPinned: value.parent_id === null || unavailable,
        isOrphan: unavailable,
        title:
          value.parent?.title ??
          (unavailable ? labels.otherParents : labels.noParent),
        identifier: value.parent?.identifier ?? "",
        parentIssue: value.parent
          ? { id: value.parent.id, status: value.parent.status as IssueStatus }
          : null,
        project: null,
        actor: null,
        matches: (issue) => issue.parent_issue_id === value.parent_id,
        moveUpdates: unavailable
          ? {}
          : { parent_issue_id: value.parent_id },
        total: descriptor.count,
        serverCellKeys,
      }];
    }
    return [];
  });
  const orderIndex = new Map<string, number>();
  storedOrder.forEach((rawId, index) => orderIndex.set(rawId, index));
  const originalIndex = new Map(
    lanes.map((lane, index) => [lane.rawId, index]),
  );
  return lanes.toSorted((a, b) => {
    if (a.isPinned !== b.isPinned) return a.isPinned ? -1 : 1;
    const ai = orderIndex.get(a.rawId);
    const bi = orderIndex.get(b.rawId);
    if (ai !== undefined && bi !== undefined) return ai - bi;
    if (ai !== undefined) return -1;
    if (bi !== undefined) return 1;
    return (originalIndex.get(a.rawId) ?? 0) -
      (originalIndex.get(b.rawId) ?? 0);
  });
}

function SwimLaneViewImpl({
  issues,
  unfilteredIssues,
  activeFilters: activeFiltersProp,
  visibleStatuses = ALL_STATUSES,
  hiddenStatuses = [],
  onMoveIssue,
  childProgressMap = EMPTY_PROGRESS_MAP,
  projectMap,
  projectId,
  onCreateIssue,
  groupBranches,
}: {
  issues: Issue[];
  /**
   * 不做状态过滤的伴生集合，用于父级元数据查找与状态总数。
   * 泳道发现仍以可见的 `issues` 为驱动——让"子任务全在隐藏状态"的父级不要
   * 产出空行——但表头装饰（编号、标题、供"打开父任务"链接用的 issue 引用）与
   * 隐藏列计数从这里读取，使处于隐藏状态的父级也能正确显示标签。
   */
  unfilteredIssues?: Issue[];
  activeFilters?: Omit<IssueFilters, "statusFilters">;
  visibleStatuses?: IssueStatusCategory[];
  hiddenStatuses?: IssueStatusCategory[];
  onMoveIssue: (
    issueId: string,
    updates: SwimLaneMoveUpdates,
    onSettled?: () => void,
  ) => void;
  childProgressMap?: Map<string, ChildProgress>;
  projectMap?: Map<string, Project>;
  /** 为格内"+"按钮预填新建表单的 `project_id`。 */
  projectId?: string;
  onCreateIssue?: (defaults: IssueCreateDefaults) => void;
  groupBranches?: IssueGroupBranches;
}) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();
  const viewStoreApi = useViewStoreApi();
  const viewBaseline = useViewBaseline();
  const sortBy = useViewStore((s) => s.sortBy);
  const sortDirection = useViewStore((s) => s.sortDirection);
  const swimlaneGrouping = useViewStore((s) => s.swimlaneGrouping);
  const swimlaneOrders = useViewStore((s) => s.swimlaneOrders);
  const swimlaneOrder = swimlaneOrders[swimlaneGrouping];

  const wsId = useWorkspaceId();
  const { categoryOf } = useIssueStatuses(wsId);

  const activeFilters = useMemo(() => ({
    // 状态由"可见列渲染"强制，不由 filterIssues 处理
    statusFilters: [],
    priorityFilters: activeFiltersProp?.priorityFilters ?? [],
    assigneeFilters: activeFiltersProp?.assigneeFilters ?? [],
    includeNoAssignee: activeFiltersProp?.includeNoAssignee ?? false,
    assigneeFilterActive: activeFiltersProp?.assigneeFilterActive ?? false,
    // 额外子任务尚不属于服务端分组页，所以这里沿用 `/api/working-agents`
    // 返回的"正在运行任务"的 issue id 集合。
    agentRunningFilter: activeFiltersProp?.agentRunningFilter ?? false,
    runningIssueIds: activeFiltersProp?.runningIssueIds,
    creatorFilters: activeFiltersProp?.creatorFilters ?? [],
    projectFilters: activeFiltersProp?.projectFilters ?? [],
    includeNoProject: activeFiltersProp?.includeNoProject ?? false,
    labelFilters: activeFiltersProp?.labelFilters ?? [],
    // 把"显示子任务"开关带到额外子任务的合并路径（见下文的
    // `filterIssues(extra, activeFilters)`）；否则即使开关关着，
    // 批量/按父级加载的子任务也会被重新加回来。
    showSubIssues: activeFiltersProp?.showSubIssues ?? true,
  }), [activeFiltersProp]);
  const projects = useMemo(
    () =>
      swimlaneGrouping === "project" && projectMap
        ? Array.from(projectMap.values())
        : EMPTY_PROJECTS,
    [projectMap, swimlaneGrouping],
  );
  const { getActorName } = useActorName();

  const laneSourceIssues = unfilteredIssues ?? issues;

  // 把规范的状态顺序（ALL_STATUSES）重新施加到"控制器标记为可见"的集合上，
  // 让各列（包括排在最后的 `cancelled`）按生命周期顺序渲染。
  const sortedStatuses = useMemo(
    () => ALL_STATUSES.filter((s) => visibleStatuses.includes(s)),
    [visibleStatuses],
  );

  const laneLabels = useMemo(
    () => ({
      noParent: t(($) => $.swimlane.no_parent),
      otherParents: t(($) => $.swimlane.other_parents),
      noProject: t(($) => $.swimlane.no_project),
      noAssignee: t(($) => $.swimlane.no_assignee),
    }),
    [t],
  );

  // 批量拉取的候选父级（仅父级分组）。并集：
  //   - 可见 issue 的 parent_issue_id（子任务已加载、父任务已翻过页）
  //   - 有子任务的可见 issue 的 id（父任务已加载、孙任务已翻过页——本 PR 修的 bug）
  const qc = useQueryClient();
  const batchParentIds = useMemo(() => {
    if (groupBranches?.enabled || swimlaneGrouping !== "parent") return [];
    const ids = new Set<string>();
    const consider = (id: string | null | undefined) => {
      if (!id) return;
      if (qc.getQueryData(issueKeys.children(wsId, id)) === undefined) {
        ids.add(id);
      }
    };
    for (const issue of issues) {
      consider(issue.parent_issue_id);
      const progress = childProgressMap.get(issue.id);
      if (progress && progress.total > 0) consider(issue.id);
    }
    return Array.from(ids).sort();
  }, [groupBranches?.enabled, swimlaneGrouping, issues, childProgressMap]); // eslint-disable-line react-hooks/exhaustive-deps

  const { data: batchChildrenMap } = useQuery(
    childrenByParentsOptions(wsId, batchParentIds, qc),
  );

  // 只增不减，这样批量键变化时泳道不会丢子任务——某个父级缓存一旦被填充就持续被
  // 观察。分组切出"父级"时重置，避免长会话里来回切换分组积累死订阅。
  const subscribedRef = useRef<Set<string>>(new Set());
  const sortedSubscribedRef = useRef<string[]>([]);
  const subscribedParentIds = useMemo(() => {
    if (groupBranches?.enabled || swimlaneGrouping !== "parent") {
      if (subscribedRef.current.size > 0) {
        subscribedRef.current = new Set();
        sortedSubscribedRef.current = [];
      }
      return sortedSubscribedRef.current;
    }
    let changed = false;
    const add = (id: string | null | undefined) => {
      if (!id || subscribedRef.current.has(id)) return;
      subscribedRef.current.add(id);
      changed = true;
    };
    for (const issue of issues) add(issue.parent_issue_id);
    if (batchChildrenMap) for (const id of batchChildrenMap.keys()) add(id);
    if (changed) sortedSubscribedRef.current = Array.from(subscribedRef.current).sort();
    return sortedSubscribedRef.current;
  }, [groupBranches?.enabled, swimlaneGrouping, issues, batchChildrenMap]);

  // 纯缓存观察者——enabled:false 不会触发任何网络请求，只在 setQueryData 写到这些键
  // 时（批量填充、乐观 mutation 或 WS 事件）触发重渲染。
  const perParentChildrenLists = useQueries({
    queries: subscribedParentIds.map((parentId) => ({
      queryKey: issueKeys.children(wsId, parentId),
      queryFn: async (): Promise<Issue[]> => [],
      enabled: false,
    })),
    combine: combineChildrenLists,
  });

  // 把分页的 issue 与批量拉取的子任务合并，让父级泳道即使子任务超出第一页也非空。
  const mergedIssues = useMemo(() => {
    if (groupBranches?.enabled) return issues;
    if (swimlaneGrouping !== "parent") return issues;
    const existingIds = new Set(issues.map((i) => i.id));
    const extra: Issue[] = [];
    const covered = new Set<string>();
    for (let i = 0; i < subscribedParentIds.length; i++) {
      const data = perParentChildrenLists[i];
      if (!data) continue;
      covered.add(subscribedParentIds[i]!);
      for (const child of data) {
        if (!existingIds.has(child.id)) {
          existingIds.add(child.id);
          extra.push(child);
        }
      }
    }
    if (batchChildrenMap) {
      for (const [parentId, children] of batchChildrenMap) {
        if (covered.has(parentId)) continue;
        for (const child of children) {
          if (!existingIds.has(child.id)) {
            existingIds.add(child.id);
            extra.push(child);
          }
        }
      }
    }
    const filteredExtra = filterIssues(extra, activeFilters);
    return filteredExtra.length === 0 ? issues : [...issues, ...filteredExtra];
  }, [groupBranches?.enabled, swimlaneGrouping, issues, perParentChildrenLists, subscribedParentIds, batchChildrenMap, activeFilters]);

  const laneGroups = useMemo<LaneGroup[]>(() => {
    if (groupBranches?.enabled) {
      return buildServerLanes(
        groupBranches.descriptors,
        swimlaneGrouping,
        sortedStatuses,
        projectMap,
        getActorName,
        swimlaneOrder,
        laneLabels,
      );
    }
    if (swimlaneGrouping === "project") {
      return buildProjectLanes(issues, projects, swimlaneOrder, laneLabels);
    }
    if (swimlaneGrouping === "assignee") {
      return buildAssigneeLanes(issues, getActorName, swimlaneOrder, laneLabels);
    }
    // 发现泳道用 `mergedIssues`，这样批量拉到的孙任务也能把父级提升成表头；
    // 元数据用 `laneSourceIssues`，让隐藏状态里的父级也能解析出表头。
    return buildParentLanes(mergedIssues, laneSourceIssues, swimlaneOrder, laneLabels);
  }, [
    swimlaneGrouping,
    issues,
    mergedIssues,
    laneSourceIssues,
    projects,
    getActorName,
    swimlaneOrder,
    laneLabels,
    groupBranches,
    projectMap,
    sortedStatuses,
  ]);

  // 父级分组下：本身是泳道头的 issue 不应再作为卡片出现（否则双重渲染）。
  // 其他分组不会这样撞车（泳道是项目/负责人，不是 issue），所以那里该集合为空。
  const headerIssueIds = useMemo(() => {
    if (swimlaneGrouping !== "parent") {
      return EMPTY_HEADER_IDS;
    }
    return new Set(
      laneGroups
        .filter((g) => g.parentIssue !== null)
        .map((g) => g.parentIssue!.id),
    );
  }, [laneGroups, swimlaneGrouping]);

  // issue id → 所属泳道键 的映射。父级分组的孤儿检测用它（规范父级不是本组表头的
  // 子任务落到回退桶里），项目/负责人分组的 matcher 热路径也用它。
  const cells = useMemo(() => {
    const result: Record<string, Record<string, string[]>> = {};
    for (const lane of laneGroups) {
      const cellMap: Record<string, string[]> = {};
      for (const status of sortedStatuses) cellMap[status] = [];
      result[lane.key] = cellMap;
    }

    const orphanLane =
      swimlaneGrouping === "parent"
        ? laneGroups.find((g) => g.isOrphan) ?? null
        : null;

    const issueSource = swimlaneGrouping === "parent" ? mergedIssues : issues;
    const sorted = sortIssues(issueSource, sortBy, sortDirection);
    for (const issue of sorted) {
      let placed = false;
      for (const lane of laneGroups) {
        if (lane.isOrphan) continue;
        if (lane.matches(issue)) {
          // 仅"无父任务"泳道：跳过本身就是泳道头的 issue（避免卡片+表头双份）。
          // 真实父级泳道保留双重渲染，这样祖父级泳道永远不会空。
          if (
            swimlaneGrouping === "parent" &&
            lane.rawId === NONE_LANE_ID &&
            headerIssueIds.has(issue.id)
          ) {
            placed = true;
            break;
          }
          // 格子是"类别"：自定义状态归入它行为对应的列；若按原始状态键作格键，
          // 那些卡片会整个从网格里消失 (MUL-6409)。
          const status = issueColumnCategory(issue);
          if (result[lane.key]?.[status]) {
            result[lane.key]![status]!.push(issue.id);
            placed = true;
            break;
          }
        }
      }
      // 父级分组：父级不是本组表头的子任务落入孤儿回退桶，以免悄悄消失。
      if (!placed && orphanLane && issue.parent_issue_id !== null) {
        const status = issueColumnCategory(issue);
        if (result[orphanLane.key]?.[status]) {
          result[orphanLane.key]![status]!.push(issue.id);
        }
      }
    }
    return result;
  }, [issues, mergedIssues, laneGroups, sortedStatuses, sortBy, sortDirection, headerIssueIds, swimlaneGrouping]);

  const laneByKey = useMemo(() => {
    const map = new Map<string, LaneGroup>();
    for (const lane of laneGroups) map.set(lane.key, lane);
    return map;
  }, [laneGroups]);

  const cellSet = useMemo(() => {
    const ids = new Set<string>();
    for (const lane of laneGroups) {
      for (const status of sortedStatuses) {
        ids.add(cellId(lane.key, status));
      }
    }
    return ids;
  }, [laneGroups, sortedStatuses]);

  // 同时驱动"可见状态表头计数"与"隐藏列面板的行"。已知限制（父级分组）：
  // `headerIssueIds` 由"当前渲染里存在的泳道"（只有子任务 ≥1 可见的父级）派生。
  // 子任务全被隐藏的父级不会得到泳道，因此在这里被计为卡片；等用户取消隐藏该状态，
  // 父级被提升为泳道头，该状态计数会 -1。记录为后续跟进项。
  const statusTotals = useMemo(() => {
    // 列是"类别"，所以总计按类别分桶：自定义状态归入它行为对应的列，
    // 绝不归入自己的那一列。
    if (groupBranches?.enabled) {
      const totals = new Map<IssueStatusCategory, number>();
      for (const lane of groupBranches.descriptors) {
        for (const cell of lane.secondary_groups ?? []) {
          if (cell.value.kind !== "status") continue;
          const category = categoryOf(cell.value.status as IssueStatus);
          totals.set(category, (totals.get(category) ?? 0) + cell.count);
        }
      }
      return totals;
    }
    const totals = new Map<IssueStatusCategory, number>();
    for (const issue of laneSourceIssues) {
      if (headerIssueIds.has(issue.id)) continue;
      const category = categoryOf(issue.status);
      totals.set(category, (totals.get(category) ?? 0) + 1);
    }
    return totals;
  }, [groupBranches, laneSourceIssues, headerIssueIds]);

  // 折叠的泳道——经 view store 按分组持久化。store 的键是原始泳道 id（固定泳道用
  // 哨兵 `NONE_LANE_ID` / `ORPHAN_LANE_ID`），在这里命名空间成 `<grouping>:<rawId>`。
  // 读写时转换，让 store 保持"不知道分组"。
  const collapsedSwimlanesMap = useViewStore((s) => s.collapsedSwimlanes);
  const collapsedLanes = useMemo(() => {
    const stored = collapsedSwimlanesMap[swimlaneGrouping] ?? [];
    const set = new Set<string>();
    for (const id of stored) set.add(`${swimlaneGrouping}:${id}`);
    return set;
  }, [collapsedSwimlanesMap, swimlaneGrouping]);
  const toggleLane = useCallback(
    (laneKey: string) => {
      const prefix = `${swimlaneGrouping}:`;
      const storeKey = laneKey.startsWith(prefix)
        ? laneKey.slice(prefix.length)
        : laneKey;
      viewStoreApi.getState().toggleSwimlaneCollapsed(storeKey);
    },
    [viewStoreApi, swimlaneGrouping],
  );

  const [activeIssue, setActiveIssue] = useState<Issue | null>(null);
  // 外层滚动盒是泳道 Virtuoso 的 customScrollParent。
  const [scrollEl, setScrollEl] = useState<HTMLDivElement | null>(null);
  // 拉式滚动恢复 (MUL-4741)：与 board/list/issue-detail 同一套接线——ref 附着在
  // 绘制前赋回保存的偏移量，泳道 Virtuoso 通过 initialScrollTop 出生在该位置。
  const restoredScrollTop = useRestoredScrollOffset("swimlane");
  const restoreScrollRef = useRestoredScrollRef("swimlane");
  const attachScroller = useCallback(
    (el: HTMLDivElement | null) => {
      setScrollEl(el);
      restoreScrollRef(el);
    },
    [restoreScrollRef],
  );
  const isDraggingRef = useRef(false);
  // 落定锁：从 drop 到移动 mutation 落定一直持有，避免中途落下的缓存变化（如成员
  // 重新拉取）在乐观移动脚下重建 localCells。与 board-view / list-view 同款。
  // settleVersion 在锁释放时强制做一次重新对账。
  const isSettlingRef = useRef(false);
  const [settleVersion, setSettleVersion] = useState(0);

  const issueMap = useMemo(() => {
    const map = new Map<string, Issue>();
    for (const issue of mergedIssues) map.set(issue.id, issue);
    return map;
  }, [mergedIssues]);

  const issueMapRef = useRef(issueMap);
  if (!isDraggingRef.current && !isSettlingRef.current) {
    issueMapRef.current = issueMap;
  }

  const [localCells, setLocalCells] = useState(cells);
  const localCellsRef = useRef(localCells);
  localCellsRef.current = localCells;

  useEffect(() => {
    if (!isDraggingRef.current && !isSettlingRef.current) {
      setLocalCells(cells);
    }
  }, [cells, settleVersion]);

  const recentlyMovedRef = useRef(false);
  useEffect(() => {
    const id = requestAnimationFrame(() => {
      recentlyMovedRef.current = false;
    });
    return () => cancelAnimationFrame(id);
  }, [localCells]);

  const collisionDetection = useMemo(
    () => makeSwimLaneCollision(cellSet),
    [cellSet],
  );

  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: { distance: 5 },
    }),
  );

  const handleDragStart = useCallback((event: DragStartEvent) => {
    isDraggingRef.current = true;
    const activeId = event.active.id as string;
    // 泳道拖动不带 Issue 负载——先清掉卡片浮层，免得重排泳道时显示出一张过期的卡片。
    if (parseLaneId(activeId) !== null) {
      setActiveIssue(null);
      return;
    }
    const issue = issueMapRef.current.get(activeId) ?? null;
    setActiveIssue(issue);
  }, []);

  const handleDragOver = useCallback(
    (event: DragOverEvent) => {
      const { active, over } = event;
      if (!over || recentlyMovedRef.current) return;

      const activeId = active.id as string;
      const overId = over.id as string;

      setLocalCells((prev) => {
        const activeCell = findCellIn(prev, cellSet, activeId);
        const overCell = findCellIn(prev, cellSet, overId);
        if (!activeCell || !overCell) return prev;
        if (
          activeCell.laneKey === overCell.laneKey &&
          activeCell.status === overCell.status
        ) {
          return prev;
        }
        // "其他父任务"泳道仅供展示：绝不让卡片经拖拽进/出它。这条泳道代表"规范父级
        // 未加载"的子任务，任何跨泳道移动要么丢掉那个父级（离开时）、要么凭空造一个
        // 新的（进入时）。
        if (
          laneByKey.get(activeCell.laneKey)?.isOrphan ||
          laneByKey.get(overCell.laneKey)?.isOrphan
        ) {
          return prev;
        }

        // 自为父级守卫——理由见 handleDragEnd。
        const overLane = laneByKey.get(overCell.laneKey);
        if (
          overLane &&
          overLane.parentIssue !== null &&
          overLane.parentIssue.id === activeId
        ) {
          return prev;
        }

        recentlyMovedRef.current = true;

        if (activeCell.laneKey === overCell.laneKey) {
          // 同一泳道行内、不同状态列之间
          const row = prev[activeCell.laneKey] ?? {};
          const sourceIds = (row[activeCell.status] ?? []).filter((id) => id !== activeId);
          const targetIds = (row[overCell.status] ?? []).filter((id) => id !== activeId);

          const overIndex = targetIds.indexOf(overId);
          const insertIndex = overIndex >= 0 ? overIndex : targetIds.length;
          targetIds.splice(insertIndex, 0, activeId);

          return {
            ...prev,
            [activeCell.laneKey]: {
              ...row,
              [activeCell.status]: sourceIds,
              [overCell.status]: targetIds,
            },
          };
        }

        // 不同的泳道行之间
        const sourceRow = prev[activeCell.laneKey] ?? {};
        const targetRow = prev[overCell.laneKey] ?? {};

        const sourceIds = (sourceRow[activeCell.status] ?? []).filter((id) => id !== activeId);
        const targetIds = (targetRow[overCell.status] ?? []).filter((id) => id !== activeId);

        const overIndex = targetIds.indexOf(overId);
        const insertIndex = overIndex >= 0 ? overIndex : targetIds.length;
        targetIds.splice(insertIndex, 0, activeId);

        return {
          ...prev,
          [activeCell.laneKey]: {
            ...sourceRow,
            [activeCell.status]: sourceIds,
          },
          [overCell.laneKey]: {
            ...targetRow,
            [overCell.status]: targetIds,
          },
        };
      });
    },
    [cellSet, laneByKey],
  );

  const handleDragEnd = useCallback(
    (event: DragEndEvent) => {
      const { active, over } = event;
      isDraggingRef.current = false;
      setActiveIssue(null);

      const reset = () => setLocalCells(cells);

      if (!over) {
        reset();
        return;
      }

      const activeId = active.id as string;
      const overId = over.id as string;

      // 泳道重排要先于卡片移动逻辑执行，因为泳道 id 无法解析到任何卡片格。
      const activeLaneRef = parseLaneId(activeId);
      const overLaneRef = parseLaneId(overId);
      if (
        activeLaneRef &&
        overLaneRef &&
        activeLaneRef.rawId !== overLaneRef.rawId
      ) {
        // 当前渲染顺序里可见的、非固定的泳道。
        const visibleOrder = laneGroups
          .filter((g) => !g.isPinned && !g.isOrphan)
          .map((g) => g.rawId);
        const fromIdx = visibleOrder.indexOf(activeLaneRef.rawId);
        const toIdx = visibleOrder.indexOf(overLaneRef.rawId);
        if (fromIdx === -1 || toIdx === -1 || fromIdx === toIdx) return;
        const visibleNext = arrayMove(visibleOrder, fromIdx, toIdx);

        // 合并进已持久化的顺序，同时不冲掉"当前不可见"的条目（比如被状态筛选藏起来
        // 的那些）。遍历 stored，把每个可见槽位替换成 `visibleNext` 的下一个 id；
        // 不可见条目原样通过。`visibleNext` 里还剩的 id（stored 里压根没有的可见
        // 泳道）统一追加到末尾。
        const stored =
          viewStoreApi.getState().swimlaneOrders[swimlaneGrouping] ?? [];
        const visibleSet = new Set(visibleOrder);
        let cursor = 0;
        const merged = stored.map((id) =>
          visibleSet.has(id) ? visibleNext[cursor++]! : id,
        );
        for (const id of visibleNext.slice(cursor)) merged.push(id);

        viewStoreApi.getState().setSwimlaneOrder(merged);
        return;
      }
      if (activeLaneRef || overLaneRef) return;

      const cols = localCellsRef.current;

      const activeCell = findCellIn(cols, cellSet, activeId);
      const overCell = findCellIn(cols, cellSet, overId);
      if (!activeCell || !overCell) {
        reset();
        return;
      }

      // "其他父任务"泳道仅供展示。只要来源格或目标格属于它就拒绝这次 drop——
      // 既不改父级（我们不知道规范父级），也不写 position（这里的兄弟分属不同父级）。
      if (
        laneByKey.get(activeCell.laneKey)?.isOrphan ||
        laneByKey.get(overCell.laneKey)?.isOrphan
      ) {
        reset();
        return;
      }

      // 自为父级守卫（仅父级分组）：拒绝把"被拖卡片"放进"以它为父级"的目标泳道。
      // 后端会以自环为由拒绝它；在客户端提前拦截，可避免"乐观移动 → 回滚"的闪烁。
      const targetLaneForGuard = laneByKey.get(overCell.laneKey);
      if (
        targetLaneForGuard &&
        targetLaneForGuard.parentIssue !== null &&
        targetLaneForGuard.parentIssue.id === activeId
      ) {
        reset();
        return;
      }

      let finalCells = cols;
      // 处理 drop 到同一目标格内时的排序重排。
      if (
        activeCell.laneKey === overCell.laneKey &&
        activeCell.status === overCell.status
      ) {
        const ids = cols[activeCell.laneKey]?.[activeCell.status];
        if (ids) {
          const oldIndex = ids.indexOf(activeId);
          const newIndex = ids.indexOf(overId);
          if (oldIndex !== -1 && newIndex !== -1 && oldIndex !== newIndex) {
            const reordered = arrayMove(ids, oldIndex, newIndex);
            finalCells = {
              ...cols,
              [activeCell.laneKey]: {
                ...cols[activeCell.laneKey],
                [activeCell.status]: reordered,
              },
            };
            setLocalCells(finalCells);
          }
        }
      }

      const finalOverCell = findCellIn(finalCells, cellSet, activeId);
      if (!finalOverCell) {
        reset();
        return;
      }

      const finalIds = finalCells[finalOverCell.laneKey]?.[finalOverCell.status] ?? [];
      const newPosition = computePosition(finalIds, activeId, issueMapRef.current);
      const currentIssue = issueMapRef.current.get(activeId);
      const targetLane = laneByKey.get(finalOverCell.laneKey);
      if (!targetLane) {
        reset();
        return;
      }

      // 格子名字是"类别"，所以"已经在这格"是一个类别层面的判断。
      const staysInCell =
        currentIssue !== undefined &&
        issueColumnCategory(currentIssue) === finalOverCell.status;
      // ……而已在某个"自定义状态"格里的卡片还要保留它的状态：如果写成格子的规范键，
      // 会把 `awaiting_response` 改写回 `in_review`——那是真正的状态变更，会启动一个
      // agent 运行 (MUL-6409)。
      const keepsStatus =
        staysInCell && currentIssue?.status !== finalOverCell.status;
      if (
        currentIssue &&
        targetLane.matches(currentIssue) &&
        staysInCell &&
        currentIssue.position === newPosition
      ) {
        return;
      }

      isSettlingRef.current = true;
      onMoveIssue(
        activeId,
        {
          ...targetLane.moveUpdates,
          ...(keepsStatus ? {} : { status: finalOverCell.status as IssueStatus }),
          position: newPosition,
          ...getMoveAnchors(finalIds, activeId),
        },
        () => {
          isSettlingRef.current = false;
          setSettleVersion((v) => v + 1);
        },
      );
    },
    [cells, cellSet, laneByKey, laneGroups, onMoveIssue, swimlaneGrouping, viewStoreApi],
  );

  // 网格模板：每状态一列，固定宽 COLUMN_WIDTH，间距 COLUMN_GAP。
  const trackWidth = sortedStatuses.length * COLUMN_WIDTH + Math.max(0, sortedStatuses.length - 1) * COLUMN_GAP;
  const gridStyle = useMemo(
    () => ({
      display: "grid",
      gridTemplateColumns: `repeat(${sortedStatuses.length}, ${COLUMN_WIDTH}px)`,
      columnGap: `${COLUMN_GAP}px`,
      width: `${trackWidth}px`,
    }) as const,
    [sortedStatuses.length, trackWidth],
  );

  // 泳道放进同一个 Virtuoso，只有上屏的泳道保持挂载。固定泳道保持前位；
  // SortableContext 仍包住整组（它的 `items` 只含非固定泳道 id，重排逻辑不变），
  // 每格的 droppable 放在常驻挂载的泳道格里。
  const orderedLanes = useMemo(
    () => [
      ...laneGroups.filter((g) => g.isPinned),
      ...laneGroups.filter((g) => !g.isPinned),
    ],
    [laneGroups],
  );
  const nonPinnedLaneIds = useMemo(
    () =>
      laneGroups
        .filter((g) => !g.isPinned)
        .map((g) => laneIdFor(swimlaneGrouping, g.rawId)),
    [laneGroups, swimlaneGrouping],
  );
  // 各状态的"加载更多"哨兵放在 Virtuoso 的 Footer 里，让它们贴着泳道列表的真末端；
  // pt-4 复刻原来 gap-4 的间距。
  const laneComponents = useMemo(
    () => ({
      Footer: () =>
        groupBranches?.enabled && groupBranches.hasMoreGroups ? (
          <div className="pt-4">
            <InfiniteScrollSentinel
              onVisible={groupBranches.loadMoreGroups}
              loading={groupBranches.isLoadingMoreGroups}
            />
          </div>
        ) : null,
    }),
    [
      groupBranches?.enabled,
      groupBranches?.hasMoreGroups,
      groupBranches?.isLoadingMoreGroups,
      groupBranches?.loadMoreGroups,
    ],
  );

  // 中断的拖拽（pointercancel、窗口缩放、标签页隐藏、Esc）会触发 onDragCancel 而
  // 不是 onDragEnd。在这里释放拖拽锁，让 localCells 之后能继续与缓存重新同步——
  // 触发这条路径的触摸场景与 list-view 的同一处理一致 (MUL-6240)。
  const handleDragCancel = useCallback(() => {
    isDraggingRef.current = false;
    setActiveIssue(null);
    setLocalCells(cells);
  }, [cells]);

  const computeLaneKey = (_index: number, lane: LaneGroup) => lane.key;
  const renderLane = (index: number, lane: LaneGroup) => (
    <div className={index === 0 ? undefined : "pt-4"}>
      <DraggableSwimLane
        lane={lane}
        grouping={swimlaneGrouping}
        isCollapsed={collapsedLanes.has(lane.key)}
        onToggleCollapse={() => toggleLane(lane.key)}
        localCells={localCells}
        sortedStatuses={sortedStatuses}
        issueMap={issueMapRef.current}
        childProgressMap={childProgressMap}
        projectMap={projectMap}
        gridStyle={gridStyle}
        paths={paths}
        projectId={projectId}
        onCreateIssue={onCreateIssue}
        groupPagination={groupBranches?.pagination}
      />
    </div>
  );

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={collisionDetection}
      onDragStart={handleDragStart}
      onDragOver={handleDragOver}
      onDragEnd={handleDragEnd}
      onDragCancel={handleDragCancel}
    >
      <div ref={attachScroller} data-tab-scroll-root="swimlane" className="flex flex-1 min-h-0 gap-4 overflow-auto p-4">
        <div className="flex shrink-0 flex-col" style={{ width: `${trackWidth}px` }}>
        {groupBranches?.isError && laneGroups.length === 0 && (
          <button
            type="button"
            className="py-8 text-body text-destructive hover:underline"
            onClick={groupBranches.retryGroups}
          >
            {t(($) => $.table.load_more_failed_retry)}
          </button>
        )}
        {/* 吸顶的状态标题行——视觉上与 BoardColumn 顶部一致 */}
        <div className="sticky top-0 z-10 mb-2 bg-background/95 pb-2 backdrop-blur supports-[backdrop-filter]:bg-background/75">
          <div style={gridStyle}>
            {sortedStatuses.map((status) => {
              const cfg = STATUS_CONFIG[statusCategoryOfKey(status)];
              const total = statusTotals.get(status) ?? 0;
              return (
                <div
                  key={status}
                  className={`flex items-center justify-between rounded-xl ${cfg?.columnBg ?? "bg-muted/40"} px-3 py-2`}
                >
                  <StatusHeading status={status} count={total} />
                  {/* 与看板列菜单一样懒挂载——见 DeferredPopup。 */}
                  <DeferredPopup
                    ariaHasPopup="menu"
                    triggerRender={
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        aria-label={t(($) => $.board.hide_column)}
                        className="rounded-full text-muted-foreground"
                      >
                        <MoreHorizontal className="size-3.5" />
                      </Button>
                    }
                  >
                    {(open, onOpenChange) => (
                      <DropdownMenu open={open} onOpenChange={onOpenChange}>
                        <DropdownMenuTrigger
                          render={
                            <Button
                              type="button"
                              variant="ghost"
                              size="icon-sm"
                              aria-label={t(($) => $.board.hide_column)}
                              className="rounded-full text-muted-foreground"
                            >
                              <MoreHorizontal className="size-3.5" />
                            </Button>
                          }
                        />
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem
                            disabled={viewBaseline?.status.has(status) === true}
                            title={
                              viewBaseline?.status.has(status) === true
                                ? t(($) => $.filters.in_view)
                                : undefined
                            }
                            onClick={() => viewStoreApi.getState().hideStatus(status)}
                          >
                            <EyeOff className="size-3.5" />
                            {t(($) => $.board.hide_column)}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    )}
                  </DeferredPopup>
                </div>
              );
            })}
          </div>
        </div>

        {/* 泳道行，虚拟化渲染。固定泳道（无-X 桶、父级分组的孤儿回退）保持前位且
            不可拖；SortableContext 仍让其余泳道能拖把手重排（其 `items` 只含非固定
            泳道 id）。只有上屏的泳道保持挂载。 */}
        <SortableContext
          items={nonPinnedLaneIds}
          strategy={verticalListSortingStrategy}
        >
          {/* 滚动 ref 在重挂载后尚未稳定时，先播种一截有界的真实泳道，避免泳道区
              刷成空白；ref 就绪后再挂载带匹配 `initialItemCount` 的 Virtuoso，
              扛过测量帧 (MUL-4750)。 */}
          {scrollEl ? (
            <Virtuoso
              customScrollParent={scrollEl}
              data={orderedLanes}
              computeItemKey={computeLaneKey}
              initialScrollTop={restoredScrollTop}
              initialItemCount={Math.min(orderedLanes.length, SWIMLANE_LANE_SEED_COUNT)}
              increaseViewportBy={{ top: 600, bottom: 600 }}
              components={laneComponents}
              itemContent={renderLane}
            />
          ) : (
            <VirtuosoSeed
              data={orderedLanes}
              itemContent={renderLane}
              computeItemKey={computeLaneKey}
              count={SWIMLANE_LANE_SEED_COUNT}
            />
          )}
        </SortableContext>
        </div>

        {hiddenStatuses.length > 0 && (
          <SwimLaneHiddenColumnsPanel
            hiddenStatuses={hiddenStatuses}
            statusTotals={statusTotals}
          />
        )}
      </div>

      <DragOverlay dropAnimation={null}>
        {activeIssue ? (
          <div className="w-[280px] rotate-2 scale-105 cursor-grabbing opacity-90 shadow-lg shadow-black/10">
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

/**
 * 渲染单条泳道（泳道头 + 一排卡片格）。
 *
 * 非固定泳道经 `useSortable` 变可拖，让用户能重排它们。固定泳道（无-X 桶、父级分组
 * 的孤儿回退）以 `disabled: true` 通过，从而保持固定、不可拖——但 `useSortable`
 * 仍须无条件调用，以满足 hooks 规则。
 *
 * 点击 vs 拖拽：`PointerSensor` 设了 `activationConstraint: { distance: 5 }`，
 * 所以点击表头仍切换折叠，而 ≥5px 的拖动才开始排序交互。"打开父任务"铅笔链接
 * 关停了指针事件，让用户点击它时不会无意触发拖拽。
 */
function DraggableSwimLane({
  lane,
  grouping,
  isCollapsed,
  onToggleCollapse,
  localCells,
  sortedStatuses,
  issueMap,
  childProgressMap,
  projectMap,
  gridStyle,
  paths,
  projectId,
  onCreateIssue,
  groupPagination,
}: {
  lane: LaneGroup;
  grouping: SwimlaneGrouping;
  isCollapsed: boolean;
  onToggleCollapse: () => void;
  localCells: Record<string, Record<string, string[]>>;
  sortedStatuses: IssueStatus[];
  issueMap: Map<string, Issue>;
  childProgressMap: Map<string, ChildProgress>;
  projectMap?: Map<string, Project>;
  gridStyle: React.CSSProperties;
  paths: ReturnType<typeof useWorkspacePaths>;
  projectId?: string;
  onCreateIssue?: (defaults: IssueCreateDefaults) => void;
  groupPagination?: Record<string, IssueGroupPageState>;
}) {
  const { t } = useT("issues");
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: laneIdFor(grouping, lane.rawId),
    disabled: lane.isPinned,
  });

  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
  };

  const laneTotal = sortedStatuses.reduce((sum, status) => {
    const serverKey = lane.serverCellKeys?.[status];
    return (
      sum +
      (serverKey
        ? (groupPagination?.[serverKey]?.total ?? 0)
        : (localCells[lane.key]?.[status]?.length ?? 0))
    );
  }, 0);

  return (
    <div ref={setNodeRef} style={style} className={`flex flex-col ${isDragging ? "opacity-50" : ""}`}>
      {/* 非交互容器——内部是独立的折叠按钮和辅助链接（如打开父任务），不能把 <a>
          嵌进 <button>。拖拽监听挂在这里，让整条表头行都是拖拽面。 */}
      <div
        className="mb-2 flex w-full items-center gap-2 rounded-md px-1 py-1"
        {...attributes}
        {...listeners}
      >
        {!lane.isPinned && (
          <GripVertical
            className="!size-3 shrink-0 cursor-grab text-faint-foreground"
            aria-hidden
          />
        )}
        <button
          type="button"
          onClick={onToggleCollapse}
          aria-label={t(($) => $.swimlane.toggle_collapse)}
          className="flex min-w-0 flex-1 items-center gap-2 rounded-md text-left transition-colors hover:bg-accent/70"
        >
          <ChevronRight
            className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${isCollapsed ? "" : "rotate-90"}`}
          />
          {lane.parentIssue && (
            <StatusIcon
              status={lane.parentIssue.status}
              category={issueStatusCategory(lane.parentIssue) ?? undefined}
              className="size-3.5"
            />
          )}
          {lane.project && <ProjectIcon project={lane.project} size="sm" />}
          {lane.actor && (
            <ActorAvatar
              actorType={lane.actor.type}
              actorId={lane.actor.id}
              size="sm"
            />
          )}
          <span className="truncate text-body font-semibold">{lane.title}</span>
          {lane.identifier && (
            <span className="shrink-0 rounded-full bg-muted px-1.5 py-0.5 text-micro font-medium tabular-nums text-muted-foreground">
              {lane.identifier}
            </span>
          )}
          <span className="shrink-0 text-caption tabular-nums text-muted-foreground">
            {laneTotal}
          </span>
        </button>
        {lane.parentIssue && (
          <DeferredTooltip
            content={t(($) => $.swimlane.open_parent)}
            trigger={
              <AppLink
                href={paths.issueDetail(lane.parentIssue.id)}
                aria-label={t(($) => $.swimlane.open_parent)}
                className="inline-flex size-5 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
              >
                <Pencil className="size-3" />
              </AppLink>
            }
          />
        )}
      </div>
      {/* 卡片格行——每格镜像一个 BoardColumn 主体 */}
      {!isCollapsed && (
        <div style={gridStyle}>
          {sortedStatuses.map((status) => {
            const cId = cellId(lane.key, status);
            const issueIds = localCells[lane.key]?.[status] ?? [];
            return (
              <SwimLaneCell
                key={cId}
                cellId={cId}
                issueIds={issueIds}
                issueMap={issueMap}
                childProgressMap={childProgressMap}
                projectMap={projectMap}
                status={status}
                lane={lane}
                projectId={projectId}
                onCreateIssue={onCreateIssue}
                readOnly={lane.isOrphan}
                page={
                  lane.serverCellKeys?.[status]
                    ? groupPagination?.[lane.serverCellKeys[status]!]
                    : undefined
                }
              />
            );
          })}
        </div>
      )}
    </div>
  );
}

function SwimLaneCell({
  cellId: cId,
  issueIds,
  issueMap,
  childProgressMap,
  projectMap,
  status,
  lane,
  projectId,
  onCreateIssue,
  readOnly = false,
  page,
}: {
  cellId: string;
  issueIds: string[];
  issueMap: Map<string, Issue>;
  childProgressMap: Map<string, ChildProgress>;
  projectMap?: Map<string, Project>;
  status: IssueStatus;
  lane: LaneGroup;
  projectId?: string;
  onCreateIssue?: (defaults: IssueCreateDefaults) => void;
  /**
   * 只读格——抑制新建入口，上游的 drag-end 也拒绝会把卡片重新锚定到这条泳道的
   * drop。用于父级分组下"内容属于未加载父级"的孤儿回退格。
   */
  readOnly?: boolean;
  page?: IssueGroupPageState;
}) {
  // 孤儿格在碰撞图中保持启用，让落到它空白区的 drop 在这里被吸收，而不是漏到
  // 最近的真实格上。真正执行移动的拦截在 handleDragOver / handleDragEnd 的
  // `isOrphan` 守卫里。
  const { setNodeRef, isOver: droppableIsOver } = useDroppable({ id: cId });
  // 只读格永不显示悬停高亮——守卫会拒绝这次 drop，视觉确认会误导人。
  const isOver = readOnly ? false : droppableIsOver;
  const { t } = useT("issues");
  const cfg = STATUS_CONFIG[statusCategoryOfKey(status)];

  const resolvedIssues = useMemo(
    () =>
      issueIds.flatMap((id) => {
        const issue = issueMap.get(id);
        return issue ? [issue] : [];
      }),
    [issueIds, issueMap],
  );

  const handleAdd = useCallback(() => {
    const data: IssueCreateDefaults = { status, ...lane.moveUpdates };
    // 按页的项目覆盖优先（例如项目详情页会预填自己的项目 id，与分组方式无关）。
    if (projectId) data.project_id = projectId;
    onCreateIssue?.(data);
  }, [status, lane, projectId, onCreateIssue]);

  return (
    <div className={`flex min-h-[120px] flex-col rounded-xl ${cfg?.columnBg ?? "bg-muted/40"} p-2`}>
      <div
        ref={setNodeRef}
        className={`flex-1 space-y-2 rounded-lg p-1 transition-colors ${
          isOver ? "bg-accent/60" : ""
        }`}
      >
        <SortableContext items={issueIds} strategy={verticalListSortingStrategy}>
          {resolvedIssues.map((issue) => (
            <DraggableBoardCard
              key={issue.id}
              issue={issue}
              childProgress={childProgressMap.get(issue.id)}
              project={
                issue.project_id ? projectMap?.get(issue.project_id) : undefined
              }
            />
          ))}
        </SortableContext>
        {issueIds.length === 0 && (
          <p className="py-6 text-center text-caption text-muted-foreground">
            &mdash;
          </p>
        )}
        {page && (
          <ListLoadMoreFooter
            hasMore={page.hasMore}
            isLoading={page.isLoading || page.isFetching}
            total={page.total}
            onLoadMore={page.loadMore}
            isError={page.isError}
            onRetry={page.retry}
          />
        )}
      </div>
      {/* 每个 lane×status 格一个（真实泳道里约 170 个）——急切挂载的 tooltip 根
          曾是泳道挂载成本里最大的一块。 */}
      {!readOnly && onCreateIssue && (
        <DeferredTooltip
          content={t(($) => $.board.add_issue_tooltip)}
          trigger={
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={t(($) => $.board.add_issue_tooltip)}
              className="mt-1 w-full rounded-md text-muted-foreground hover:text-foreground"
              onClick={handleAdd}
            >
              <Plus className="size-3.5" />
            </Button>
          }
        />
      )}
    </div>
  );
}

function SwimLaneHiddenColumnsPanel({
  hiddenStatuses,
  statusTotals,
}: {
  hiddenStatuses: IssueStatusCategory[];
  statusTotals: Map<IssueStatusCategory, number>;
}) {
  return (
    <HiddenColumnsPanel
      hiddenStatuses={hiddenStatuses}
      renderRow={(status) => (
        <HiddenColumnRow
          key={status}
          status={status}
          total={statusTotals.get(status) ?? 0}
        />
      )}
    />
  );
}


/**
 * 已 memo 化：surface 控制器会在加载标志翻转时重渲染（例如视图变化让某条查询被
 * 启用）——没有 memo，每次这种翻转都会让整棵视图树重渲染（数百毫秒）。所有 props
 * 都是控制器产出的、引用稳定的 useMemo/useCallback 结果。
 */
export const SwimLaneView = memo(SwimLaneViewImpl);
