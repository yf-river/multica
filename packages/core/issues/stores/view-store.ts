"use client";

// 议题视图状态的核心定义与默认工厂：/issues 页、My Issues、项目页、actor 面板、
// 已保存视图等都复用这里的视图状态形状与默认值。
// 视图状态里既含"查询定义类"过滤字段（状态/优先级/负责人/项目/标签/属性过滤等），
// 也含"纯展示类"字段（列隐藏、泳道顺序、表格列、卡片属性等）；日期与
// "agent 正在工作"过滤刻意不持久化（见 viewStorePersistOptions）。
import { useEffect, useRef } from "react";
import { create } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import { createJSONStorage, persist } from "zustand/middleware";
import type { IssueStatus, IssueStatusCategory, IssuePriority, PropertyFilterValue } from "../../types";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

/** 议题视图的展示形态：看板 / 列表 / 表格 / 甘特图 / 泳道。 */
export type ViewMode = "board" | "list" | "table" | "gantt" | "swimlane";
/** 甘特图的缩放粒度。 */
export type GanttZoom = "day" | "week" | "month";
/**
 * 看板的分组维度。除三种内置维度外，选择型自定义属性可用
 * `property:<definitionId>` 形式按它的选项分组。
 * 持久化的值可能引用一个早已归档的属性定义——消费者解析不到时必须回退到 "status"。
 */
export type IssueGrouping =
  | "status"
  | "assignee"
  | "project"
  | `property:${string}`;
/** 泳道的分组维度。 */
export type SwimlaneGrouping = "parent" | "project" | "assignee";
/**
 * 排序键。`property:<definitionId>` 由服务端对着当前属性目录解析；
 * 过期或不支持的属性定义退化成按 position 排序。
 */
export type SortField =
  | "position"
  | "status"
  | "priority"
  | "start_date"
  | "due_date"
  | "created_at"
  | "updated_at"
  | "title"
  | `property:${string}`;
/** 排序方向。 */
export type SortDirection = "asc" | "desc";
/** 可按日期过滤的字段。 */
export type IssueDateField = "created_at" | "updated_at";

/** 表格里内置（非自定义属性）的列 key。 */
export type TableSystemColumnKey =
  | "title"
  | "identifier"
  | "status"
  | "priority"
  | "assignee"
  | "labels"
  | "project"
  | "start_date"
  | "due_date"
  | "created_at"
  | "updated_at"
  | "child_progress"
  | "creator";
/** 表格列 key：内置列或自定义属性列（`property:<definitionId>`）。 */
export type TableColumnKey = TableSystemColumnKey | `property:${string}`;
/** 单列表格的列配置：key + 可选宽度。 */
export interface TableColumnConfig {
  key: TableColumnKey;
  width?: number;
}
/** 表格的分组维度：不分 / 状态 / 负责人 / 项目 / 自定义属性。 */
export type TableGrouping =
  | "none"
  | "status"
  | "assignee"
  | "project"
  | `property:${string}`;
/** 分组列底部的汇总计算。 */
export type TableCalculation = "none" | "sum" | "average" | "count";

/** 表格所有可选的内置列，按固定展示顺序排列。 */
export const TABLE_SYSTEM_COLUMNS: readonly TableSystemColumnKey[] = [
  "title",
  "identifier",
  "status",
  "priority",
  "assignee",
  "labels",
  "project",
  "start_date",
  "due_date",
  "created_at",
  "updated_at",
  "child_progress",
  "creator",
];

/** 新建表格视图时的默认列（含初始宽度）。 */
export const DEFAULT_TABLE_COLUMNS: readonly TableColumnConfig[] = [
  { key: "title", width: 360 },
  { key: "status", width: 150 },
  { key: "priority", width: 130 },
  { key: "assignee", width: 180 },
  { key: "due_date", width: 140 },
  { key: "labels", width: 220 },
];

/** 日期范围过滤：基于哪个字段 + 起止 "YYYY-MM-DD"。 */
export interface IssueDateFilter {
  field: IssueDateField;
  from: string;
  to: string;
}

/** 泳道可选的三个分组维度。 */
export const SWIMLANE_GROUPINGS: SwimlaneGrouping[] = ["parent", "project", "assignee"];

/** 看板卡片上可开关显示的内置字段。 */
export interface CardProperties {
  priority: boolean;
  description: boolean;
  assignee: boolean;
  startDate: boolean;
  dueDate: boolean;
  project: boolean;
  childProgress: boolean;
  labels: boolean;
}

/** 过滤里的"某人"取值：类型（成员/Agent/小组）+ id。 */
export interface ActorFilterValue {
  type: "member" | "agent" | "squad";
  id: string;
}

/** 九个"查询定义"过滤字段打包成一个值——已保存视图"钉住"的正是它，
 *  "重置"恢复的也正是它。 */
export interface FilterSnapshot {
  statusFilters: IssueStatus[];
  priorityFilters: IssuePriority[];
  assigneeFilters: ActorFilterValue[];
  includeNoAssignee: boolean;
  creatorFilters: ActorFilterValue[];
  projectFilters: string[];
  includeNoProject: boolean;
  labelFilters: string[];
  propertyFilters: Record<string, PropertyFilterValue[]>;
}

/** 过滤栏上每个胶囊（chip）对应的"过滤维度"。日期不在其中：`dateFilter` 活在
 *  持久化切片之外，通过 `setDateFilter(null)` 清空。 */
export type FilterDimension =
  | "status"
  | "priority"
  | "assignee"
  | "creator"
  | "project"
  | "label"
  | `property:${string}`;

/** 视图 key 里自定义属性形式的统一前缀。 */
export const PROPERTY_VIEW_PREFIX = "property:";

/** 从视图 key 里解析出自定义属性定义 id；不是属性形式则返回 null。 */
export function propertyIdFromViewKey(key: string): string | null {
  return key.startsWith(PROPERTY_VIEW_PREFIX) ? key.slice(PROPERTY_VIEW_PREFIX.length) : null;
}

/** 不含自定义属性的排序字段（界面静态选项用）。 */
export type StaticSortField = Exclude<SortField, `property:${string}`>;
/** 不含自定义属性的看板分组（界面静态选项用）。 */
export type StaticIssueGrouping = Exclude<IssueGrouping, `property:${string}`>;

/** 排序下拉的静态选项。 */
export const SORT_OPTIONS: { value: StaticSortField; label: string }[] = [
  { value: "position", label: "Manual" },
  { value: "status", label: "Status" },
  { value: "priority", label: "Priority" },
  { value: "start_date", label: "Start date" },
  { value: "due_date", label: "Due date" },
  { value: "created_at", label: "Created date" },
  { value: "updated_at", label: "Updated date" },
  { value: "title", label: "Title" },
];

/** 看板分组下拉的静态选项。 */
export const GROUPING_OPTIONS: { value: StaticIssueGrouping; label: string }[] = [
  { value: "status", label: "Status" },
  { value: "assignee", label: "Assignee" },
  { value: "project", label: "Project" },
];

/** 卡片属性开关的静态选项（含展示文案）。 */
export const CARD_PROPERTY_OPTIONS: { key: keyof CardProperties; label: string }[] = [
  { key: "priority", label: "Priority" },
  { key: "description", label: "Description" },
  { key: "assignee", label: "Assignee" },
  { key: "startDate", label: "Start date" },
  { key: "dueDate", label: "Due date" },
  { key: "project", label: "Project" },
  { key: "labels", label: "Labels" },
  { key: "childProgress", label: "Sub-issue progress" },
];

/** 议题视图的完整状态：展示形态 + 各类过滤 + 视图布局偏好 + 全部操作方法。 */
export interface IssueViewState {
  /** 当前视图形态（看板/列表/表格/甘特/泳道）。 */
  viewMode: ViewMode;
  /** 看板的分组维度。 */
  grouping: IssueGrouping;
  /** 按状态过滤：选中的状态 key 列表（空 = 不过滤）。 */
  statusFilters: IssueStatus[];
  /** 按优先级过滤。 */
  priorityFilters: IssuePriority[];
  /** 按负责人过滤（成员/Agent/小组）。 */
  assigneeFilters: ActorFilterValue[];
  /** 是否同时包含"未分配"（配合负责人过滤用）。 */
  includeNoAssignee: boolean;
  /** 按创建者过滤。 */
  creatorFilters: ActorFilterValue[];
  /** 按项目过滤。 */
  projectFilters: string[];
  /** 是否同时包含"无项目"。 */
  includeNoProject: boolean;
  /** 按标签过滤。 */
  labelFilters: string[];
  /**
   * 自定义属性过滤：定义 id → 选中的取值。复选框型属性用伪选项 "true"/"false"；
   * 标量属性存提交的裸字符串或按 `PropertyFilterValue` 的操作符对象，外加
   * "__none__" 哨兵。空数组 = 对该属性不过滤；同属性内 OR、跨属性 AND，
   * 与其它过滤组一致。
   */
  propertyFilters: Record<string, PropertyFilterValue[]>;
  /** 日期范围过滤（本身不持久化，见 viewStorePersistOptions）。 */
  dateFilter: IssueDateFilter | null;
  // 为 true 时列表只显示当前至少有 1 个 agent 任务处于 `running` 状态的议题。
  // 驱动议题页头部的"agents working"快捷过滤芯片。刷新后不保留——运行状态秒级
  // 变化，持久化的开关会让用户回到一个说不清原因的空列表。
  agentRunningFilter: boolean;
  /** 排序键。 */
  sortBy: SortField;
  /** 排序方向。 */
  sortDirection: SortDirection;
  /** 看板/列表卡片上内置字段的显隐。 */
  cardProperties: CardProperties;
  /** 看板/列表卡片上额外渲染取值的工作区自定义属性定义 id。 */
  cardPropertyIds: string[];
  // 为 false 时，有父议题的议题（子议题）从看板/列表/泳道里隐藏，让用户专注
  // 顶层父议题。纯展示过滤——从不改动父子关系本身。
  showSubIssues: boolean;
  /** 列表形态下被折叠（收起）的状态分类。 */
  listCollapsedStatuses: IssueStatusCategory[];
  /**
   * 用户隐藏的看板/列表列，按"状态分类"存。
   *
   * 列可见性以前是把存活的 status 写进 `statusFilters` 来表达——一旦某个分类能容纳
   * 多个状态这就不再正确：隐藏 Backlog 会把其它 6 个内置 key 都写进去，顺带把每个
   * 自定义状态也过滤掉了。展示状态与精确 key 过滤是两个问题，现在各用各的字段。
   * (MUL-6243)
   */
  hiddenStatusCategories: IssueStatusCategory[];
  /** 甘特图缩放粒度。 */
  ganttZoom: GanttZoom;
  /** 甘特图是否显示已完成议题。 */
  ganttShowCompleted: boolean;
  /** 当前激活的泳道分组维度。 */
  swimlaneGrouping: SwimlaneGrouping;
  /** 持久化的泳道顺序，按分组维度存。条目是原始泳道 id
   *  （父议题 id、项目 id、或 `<assigneeType>:<assigneeId>`）。 */
  swimlaneOrders: Record<SwimlaneGrouping, string[]>;
  /** 持久化的折叠泳道，按分组维度存。与 `swimlaneOrders` 同一 id 空间，另有
   *  哨兵 `"none"`（表示"无 X"泳道被钉住）和 `"__orphans__"`（父分组回退）。 */
  collapsedSwimlanes: Record<SwimlaneGrouping, string[]>;
  /** 有序的表格列。title 必在且被归一化到最前。 */
  tableColumns: TableColumnConfig[];
  /** 表格的分组维度。 */
  tableGrouping: TableGrouping;
  /** 表格里被折叠的分组 key。 */
  tableCollapsedGroups: string[];
  /** 表格层级结构里被折叠的父议题 id。 */
  tableCollapsedParents: string[];
  /** 表格是否按父子层级展开。 */
  tableHierarchy: boolean;
  /** 分组底部显示的汇总计算。 */
  tableCalculation: TableCalculation;
  /** 切换视图形态。 */
  setViewMode: (mode: ViewMode) => void;
  /** 设置甘特缩放粒度。 */
  setGanttZoom: (zoom: GanttZoom) => void;
  /** 切换甘特图是否显示已完成。 */
  toggleGanttShowCompleted: () => void;
  /** 设置看板分组维度。 */
  setGrouping: (grouping: IssueGrouping) => void;
  /** 开/关一个状态过滤。 */
  toggleStatusFilter: (status: IssueStatus) => void;
  /** 开/关一个优先级过滤。 */
  togglePriorityFilter: (priority: IssuePriority) => void;
  /** 开/关一个负责人过滤。 */
  toggleAssigneeFilter: (value: ActorFilterValue) => void;
  /** 切换"是否包含未分配"。 */
  toggleNoAssignee: () => void;
  /** 开/关一个创建者过滤。 */
  toggleCreatorFilter: (value: ActorFilterValue) => void;
  /** 开/关一个项目过滤。 */
  toggleProjectFilter: (projectId: string) => void;
  /** 切换"是否包含无项目"。 */
  toggleNoProject: () => void;
  /** 开/关一个标签过滤。 */
  toggleLabelFilter: (labelId: string) => void;
  /** 开/关某个自定义属性下的某个选项过滤。 */
  togglePropertyFilter: (propertyId: string, optionId: string) => void;
  /** 整体替换某属性的过滤取值集合（文本/数字/日期/url 等标量输入用，会拼进
   *  "__none__"）。 */
  setPropertyFilterValues: (propertyId: string, optionIds: PropertyFilterValue[]) => void;
  /** 设置日期范围过滤；传 null 清除。 */
  setDateFilter: (filter: IssueDateFilter | null) => void;
  /** 切换"只显示正在跑 agent 的议题"（该过滤不持久化）。 */
  toggleAgentRunningFilter: () => void;
  /** 隐藏某状态分类对应的一列。 */
  hideStatus: (category: IssueStatusCategory) => void;
  /** 重新显示某状态分类对应的一列。 */
  showStatus: (category: IssueStatusCategory) => void;
  /** 清空全部过滤（保留布局与排序）。 */
  clearFilters: () => void;
  /** 清空某一维度的过滤（过滤栏的一个胶囊）。`property:<id>` 只清该属性条目；
   *  成对的布尔开关（未分配/无项目）随其维度一并清。 */
  clearFilterDimension: (dimension: FilterDimension) => void;
  /** 一次性替换所有过滤字段——"在已保存视图里重置"因此回到视图自己的条件，
   *  而不是清成空。 */
  resetFiltersTo: (snapshot: FilterSnapshot) => void;
  /** 设置排序键。 */
  setSortBy: (field: SortField) => void;
  /** 设置排序方向。 */
  setSortDirection: (dir: SortDirection) => void;
  /** 切换卡片某内置字段的显示。 */
  toggleCardProperty: (key: keyof CardProperties) => void;
  /** 切换卡片某自定义属性的显示。 */
  toggleCardPropertyId: (propertyId: string) => void;
  /** 切换是否在列表形态里显示子议题。 */
  toggleShowSubIssues: () => void;
  /** 切换某状态分类在列表形态下是否折叠。 */
  toggleListCollapsed: (category: IssueStatusCategory) => void;
  /** 设置当前泳道的分组维度。 */
  setSwimlaneGrouping: (grouping: SwimlaneGrouping) => void;
  /** 更新当前泳道分组下的泳道顺序。 */
  setSwimlaneOrder: (order: string[]) => void;
  /** 切换当前泳道分组下某个泳道的折叠。 */
  toggleSwimlaneCollapsed: (key: string) => void;
  /** 开/关一个表格列。 */
  toggleTableColumn: (key: TableColumnKey) => void;
  /** 把一个表格列挪到另一个列的位置（拖拽重排）。 */
  reorderTableColumn: (active: TableColumnKey, over: TableColumnKey) => void;
  /** 设置某表格列的宽度；传 undefined 恢复自适应。 */
  setTableColumnWidth: (key: TableColumnKey, width?: number) => void;
  /** 设置表格分组维度。 */
  setTableGrouping: (grouping: TableGrouping) => void;
  /** 切换表格某分组是否折叠。 */
  toggleTableGroupCollapsed: (key: string) => void;
  /** 切换表格层级里某父议题是否折叠。 */
  toggleTableParentCollapsed: (issueId: string) => void;
  /** 切换表格是否按父子层级展开。 */
  toggleTableHierarchy: () => void;
  /** 设置分组底部的汇总计算。 */
  setTableCalculation: (calculation: TableCalculation) => void;
}

/** 视图状态切片：默认值 + 全部操作方法的一次性实现，供各 view store 复用来构建状态。 */
export const viewStoreSlice = (set: StoreApi<IssueViewState>["setState"]): IssueViewState => ({
  viewMode: "board",
  grouping: "status",
  statusFilters: [],
  priorityFilters: [],
  assigneeFilters: [],
  includeNoAssignee: false,
  creatorFilters: [],
  projectFilters: [],
  includeNoProject: false,
  labelFilters: [],
  propertyFilters: {},
  dateFilter: null,
  agentRunningFilter: false,
  sortBy: "position",
  sortDirection: "asc",
  cardProperties: {
    priority: true,
    description: true,
    assignee: true,
    startDate: true,
    dueDate: true,
    project: true,
    childProgress: true,
    labels: true,
  },
  cardPropertyIds: [],
  showSubIssues: true,
  listCollapsedStatuses: [],
  hiddenStatusCategories: [],
  ganttZoom: "week",
  ganttShowCompleted: false,
  swimlaneGrouping: "assignee",
  swimlaneOrders: { parent: [], project: [], assignee: [] },
  collapsedSwimlanes: { parent: [], project: [], assignee: [] },
  tableColumns: DEFAULT_TABLE_COLUMNS.map((column) => ({ ...column })),
  tableGrouping: "none",
  tableCollapsedGroups: [],
  tableCollapsedParents: [],
  tableHierarchy: true,
  tableCalculation: "none",

  setViewMode: (mode) => set({ viewMode: mode }),
  setGanttZoom: (zoom) => set({ ganttZoom: zoom }),
  toggleGanttShowCompleted: () =>
    set((state) => ({ ganttShowCompleted: !state.ganttShowCompleted })),
  setGrouping: (grouping) => set({ grouping }),
  toggleStatusFilter: (status) =>
    set((state) => ({
      statusFilters: state.statusFilters.includes(status)
        ? state.statusFilters.filter((s) => s !== status)
        : [...state.statusFilters, status],
    })),
  togglePriorityFilter: (priority) =>
    set((state) => ({
      priorityFilters: state.priorityFilters.includes(priority)
        ? state.priorityFilters.filter((p) => p !== priority)
        : [...state.priorityFilters, priority],
    })),
  toggleAssigneeFilter: (value) =>
    set((state) => {
      const exists = state.assigneeFilters.some(
        (f) => f.type === value.type && f.id === value.id,
      );
      return {
        assigneeFilters: exists
          ? state.assigneeFilters.filter(
              (f) => !(f.type === value.type && f.id === value.id),
            )
          : [...state.assigneeFilters, value],
      };
    }),
  toggleNoAssignee: () =>
    set((state) => ({ includeNoAssignee: !state.includeNoAssignee })),
  toggleCreatorFilter: (value) =>
    set((state) => {
      const exists = state.creatorFilters.some(
        (f) => f.type === value.type && f.id === value.id,
      );
      return {
        creatorFilters: exists
          ? state.creatorFilters.filter(
              (f) => !(f.type === value.type && f.id === value.id),
            )
          : [...state.creatorFilters, value],
      };
    }),
  toggleProjectFilter: (projectId) =>
    set((state) => ({
      projectFilters: state.projectFilters.includes(projectId)
        ? state.projectFilters.filter((id) => id !== projectId)
        : [...state.projectFilters, projectId],
    })),
  toggleNoProject: () =>
    set((state) => ({ includeNoProject: !state.includeNoProject })),
  toggleLabelFilter: (labelId) =>
    set((state) => ({
      labelFilters: state.labelFilters.includes(labelId)
        ? state.labelFilters.filter((id) => id !== labelId)
        : [...state.labelFilters, labelId],
    })),
  togglePropertyFilter: (propertyId, optionId) =>
    set((state) => {
      const current = state.propertyFilters[propertyId] ?? [];
      const next = current.includes(optionId)
        ? current.filter((id) => id !== optionId)
        : [...current, optionId];
      const propertyFilters = { ...state.propertyFilters };
      if (next.length === 0) delete propertyFilters[propertyId];
      else propertyFilters[propertyId] = next;
      return { propertyFilters };
    }),
  setPropertyFilterValues: (propertyId, optionIds) =>
    set((state) => {
      const propertyFilters = { ...state.propertyFilters };
      if (optionIds.length === 0) delete propertyFilters[propertyId];
      else propertyFilters[propertyId] = optionIds;
      return { propertyFilters };
    }),
  setDateFilter: (filter) => set({ dateFilter: filter }),
  toggleAgentRunningFilter: () =>
    set((state) => ({ agentRunningFilter: !state.agentRunningFilter })),
  hideStatus: (category) =>
    set((state) =>
      state.hiddenStatusCategories.includes(category)
        ? state
        : { hiddenStatusCategories: [...state.hiddenStatusCategories, category] },
    ),
  showStatus: (category) =>
    set((state) => ({
      hiddenStatusCategories: state.hiddenStatusCategories.filter((c) => c !== category),
    })),
  clearFilters: () =>
    set({
      statusFilters: [],
      priorityFilters: [],
      assigneeFilters: [],
      includeNoAssignee: false,
      creatorFilters: [],
      projectFilters: [],
      includeNoProject: false,
      labelFilters: [],
      propertyFilters: {},
      dateFilter: null,
      agentRunningFilter: false,
      // 重置把所有列都恢复出来——与当年"隐藏列 = 写状态过滤"时代的行为一致。
      hiddenStatusCategories: [],
    }),
  resetFiltersTo: (snapshot) => set({ ...snapshot }),
  clearFilterDimension: (dimension) =>
    set((state) => {
      switch (dimension) {
        case "status":
          return { statusFilters: [] };
        case "priority":
          return { priorityFilters: [] };
        case "assignee":
          return { assigneeFilters: [], includeNoAssignee: false };
        case "creator":
          return { creatorFilters: [] };
        case "project":
          return { projectFilters: [], includeNoProject: false };
        case "label":
          return { labelFilters: [] };
        default: {
          const propertyId = propertyIdFromViewKey(dimension);
          if (!propertyId || !(propertyId in state.propertyFilters)) return state;
          const propertyFilters = { ...state.propertyFilters };
          delete propertyFilters[propertyId];
          return { propertyFilters };
        }
      }
    }),
  setSortBy: (field) => set({ sortBy: field }),
  setSortDirection: (dir) => set({ sortDirection: dir }),
  toggleCardProperty: (key) =>
    set((state) => ({
      cardProperties: {
        ...state.cardProperties,
        [key]: !state.cardProperties[key],
      },
    })),
  toggleCardPropertyId: (propertyId) =>
    set((state) => ({
      cardPropertyIds: state.cardPropertyIds.includes(propertyId)
        ? state.cardPropertyIds.filter((id) => id !== propertyId)
        : [...state.cardPropertyIds, propertyId],
    })),
  toggleShowSubIssues: () =>
    set((state) => ({ showSubIssues: !state.showSubIssues })),
  toggleListCollapsed: (status) =>
    set((state) => ({
      listCollapsedStatuses: state.listCollapsedStatuses.includes(status)
        ? state.listCollapsedStatuses.filter((s) => s !== status)
        : [...state.listCollapsedStatuses, status],
    })),
  setSwimlaneGrouping: (grouping) => set({ swimlaneGrouping: grouping }),
  setSwimlaneOrder: (order) =>
    set((state) => ({
      swimlaneOrders: { ...state.swimlaneOrders, [state.swimlaneGrouping]: order },
    })),
  toggleSwimlaneCollapsed: (key) =>
    set((state) => {
      const grouping = state.swimlaneGrouping;
      const current = state.collapsedSwimlanes[grouping];
      const next = current.includes(key)
        ? current.filter((k) => k !== key)
        : [...current, key];
      return {
        collapsedSwimlanes: { ...state.collapsedSwimlanes, [grouping]: next },
      };
    }),
  toggleTableColumn: (key) =>
    set((state) => {
      if (key === "title") return state;
      const exists = state.tableColumns.some((column) => column.key === key);
      return {
        tableColumns: exists
          ? state.tableColumns.filter((column) => column.key !== key)
          : [...state.tableColumns, { key }],
      };
    }),
  reorderTableColumn: (active, over) =>
    set((state) => {
      if (active === "title" || over === "title" || active === over) return state;
      const from = state.tableColumns.findIndex((column) => column.key === active);
      const to = state.tableColumns.findIndex((column) => column.key === over);
      if (from < 0 || to < 0) return state;
      const tableColumns = [...state.tableColumns];
      const [moved] = tableColumns.splice(from, 1);
      if (!moved) return state;
      tableColumns.splice(to, 0, moved);
      return { tableColumns };
    }),
  setTableColumnWidth: (key, width) =>
    set((state) => ({
      tableColumns: state.tableColumns.map((column) =>
        column.key === key
          ? { ...column, ...(width === undefined ? { width: undefined } : { width }) }
          : column,
      ),
    })),
  setTableGrouping: (tableGrouping) => set({ tableGrouping }),
  toggleTableGroupCollapsed: (key) =>
    set((state) => ({
      tableCollapsedGroups: state.tableCollapsedGroups.includes(key)
        ? state.tableCollapsedGroups.filter((item) => item !== key)
        : [...state.tableCollapsedGroups, key],
    })),
  toggleTableParentCollapsed: (issueId) =>
    set((state) => ({
      tableCollapsedParents: state.tableCollapsedParents.includes(issueId)
        ? state.tableCollapsedParents.filter((id) => id !== issueId)
        : [...state.tableCollapsedParents, issueId],
    })),
  toggleTableHierarchy: () =>
    set((state) => ({ tableHierarchy: !state.tableHierarchy })),
  setTableCalculation: (tableCalculation) => set({ tableCalculation }),
});

/** 生成某视图 store 的 persist 选项（存储 key / 存储介质 / 序列化范围 / 合并规则）。 */
export const viewStorePersistOptions = (name: string) => ({
  name,
  storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
  partialize: (state: IssueViewState) => ({
    // 注意：`agentRunningFilter` 刻意不持久化——运行状态秒级变化，存下来的开关会让
    // 用户回到一个说不清原因的空列表。让它保持临时性。见 IssueViewState 里字段注释。
    // `dateFilter` 同样刻意不持久化：像"今天"这种相对预设会在跨日历日后变陈旧。
    viewMode: state.viewMode,
    grouping: state.grouping,
    statusFilters: state.statusFilters,
    priorityFilters: state.priorityFilters,
    assigneeFilters: state.assigneeFilters,
    includeNoAssignee: state.includeNoAssignee,
    creatorFilters: state.creatorFilters,
    projectFilters: state.projectFilters,
    includeNoProject: state.includeNoProject,
    labelFilters: state.labelFilters,
    propertyFilters: state.propertyFilters,
    sortBy: state.sortBy,
    sortDirection: state.sortDirection,
    cardProperties: state.cardProperties,
    cardPropertyIds: state.cardPropertyIds,
    showSubIssues: state.showSubIssues,
    listCollapsedStatuses: state.listCollapsedStatuses,
    hiddenStatusCategories: state.hiddenStatusCategories,
    ganttZoom: state.ganttZoom,
    ganttShowCompleted: state.ganttShowCompleted,
    swimlaneGrouping: state.swimlaneGrouping,
    swimlaneOrders: state.swimlaneOrders,
    collapsedSwimlanes: state.collapsedSwimlanes,
    tableColumns: state.tableColumns,
    tableGrouping: state.tableGrouping,
    tableCollapsedGroups: state.tableCollapsedGroups,
    tableCollapsedParents: state.tableCollapsedParents,
    tableHierarchy: state.tableHierarchy,
    tableCalculation: state.tableCalculation,
  }),
  // Zustand 默认的 merge 是浅合并：在新开关加入之前保存的 `cardProperties` 快照
  // 会整个赢下，新 key 缺失——下拉开关读到 `undefined` 就渲染成未选中，哪怕默认值
  // 把它当开。对 `cardProperties` 深合并，让新加的开关为存量用户继承默认值。
  merge: mergeViewStatePersisted,
});

/**
 * 视图状态 store 复用的 persist `merge` 函数。泛型于 T，让同一份 `cardProperties`
 * 深合并同时适用于 issues 视图 store 与 my-issues 视图 store（后者继承 IssueViewState）。
 */
export function mergeViewStatePersisted<T extends IssueViewState>(
  persisted: unknown,
  current: T,
): T {
  const p = (persisted ?? {}) as Partial<T>;
  // `collapsedSwimlanes` 的形状从 `string[]` 变成过 `Record<SwimlaneGrouping, string[]>`。
  // 旧形状保存的快照若直接覆盖默认的 record，会在首次读取时崩溃——持久化值不是
  // 普通对象时回退到默认。
  const isRecord = (v: unknown): v is Record<string, unknown> =>
    v !== null && typeof v === "object" && !Array.isArray(v);
  // 表格列清洗：丢弃畸形行、按 key 去重、把必有的 title 列提到最前。
  const persistedTableColumns = Array.isArray(p.tableColumns)
    ? p.tableColumns.filter(
        (column): column is TableColumnConfig =>
          !!column &&
          typeof column === "object" &&
          typeof (column as TableColumnConfig).key === "string",
      )
    : current.tableColumns;
  const dedupedTableColumns = Array.from(
    new Map(persistedTableColumns.map((column) => [column.key, column])).values(),
  ).filter((column) => column.key !== "title");
  const persistedTitle = persistedTableColumns.find(
    (column) => column.key === "title",
  );
  return {
    ...current,
    ...p,
    cardProperties: {
      ...current.cardProperties,
      ...(p.cardProperties ?? {}),
    },
    swimlaneOrders: isRecord(p.swimlaneOrders)
      ? { ...current.swimlaneOrders, ...p.swimlaneOrders }
      : current.swimlaneOrders,
    collapsedSwimlanes: isRecord(p.collapsedSwimlanes)
      ? { ...current.collapsedSwimlanes, ...p.collapsedSwimlanes }
      : current.collapsedSwimlanes,
    tableColumns: [
      persistedTitle ?? current.tableColumns[0] ?? { key: "title" },
      ...dedupedTableColumns,
    ],
    tableCollapsedGroups: Array.isArray(p.tableCollapsedGroups)
      ? p.tableCollapsedGroups
      : current.tableCollapsedGroups,
    tableCollapsedParents: Array.isArray(p.tableCollapsedParents)
      ? p.tableCollapsedParents
      : current.tableCollapsedParents,
  };
}

/** 工厂：为 React Context 使用创建一个带持久化的 vanilla StoreApi。 */
export function createIssueViewStore(persistKey: string): StoreApi<IssueViewState> {
  const store = createStore<IssueViewState>()(
    persist(viewStoreSlice, viewStorePersistOptions(persistKey))
  );
  registerForWorkspaceRehydration(() => store.persist.rehydrate());
  return store;
}

/** /issues 页使用的全局单例视图 store。 */
export const useIssueViewStore = create<IssueViewState>()(
  persist(viewStoreSlice, viewStorePersistOptions("multica_issues_view"))
);

// 切换工作区后重水合 /issues 页的视图状态。
registerForWorkspaceRehydration(() => useIssueViewStore.persist.rehydrate());

/**
 * 工作区 id 变化时清空给定视图 store 的过滤。
 *
 * 由 URL 驱动：wsId 来自 `useWorkspaceId()`（Context，由 `[workspaceSlug]` 路由喂）。
 * 用 ref 记上一个 id，保证首次渲染不清掉持久化的过滤——只在"从一个已定义工作区
 * 切到另一个"时才触发清空。
 */
export function useClearFiltersOnWorkspaceChange(
  store: StoreApi<IssueViewState> | { getState: () => IssueViewState },
  wsId: string | undefined,
) {
  const prevIdRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (prevIdRef.current && wsId && wsId !== prevIdRef.current) {
      store.getState().clearFilters();
    }
    prevIdRef.current = wsId;
  }, [wsId, store]);
}
