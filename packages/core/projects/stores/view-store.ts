// 本文件实现 projects 列表页的「视图偏好」store：紧凑表格/宽松卡片两种视图、
// 排序、列可见性与过滤。按工作区、按用户/设备持久化。无 scope（lead 可选且
// 常是 agent，没有强的个人维度；status 是更适合做成过滤器的五值生命周期）。
// 搜索保持会话级（不持久化）。

"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

// 项目是唯一的「双视图」列表：紧凑表格 + 卡片网格，由 viewMode 切换。
// 排序 + 过滤同时喂给两种视图；hiddenColumns 只作用于表格。无 scope（lead
// 可选且常是 agent，没有强的个人维度；status 是更适合做成过滤器的五值
// 生命周期）。搜索保持会话级。
export type ProjectViewMode = "compact" | "comfortable";

export type ProjectSortField = "name" | "priority" | "status" | "progress" | "created";

export type ProjectSortDirection = "asc" | "desc";

/** 用户切到某个字段时套用的默认方向。 */
export const PROJECT_SORT_DEFAULT_DIRECTION: Record<
  ProjectSortField,
  ProjectSortDirection
> = {
  name: "asc",
  priority: "desc",
  status: "asc",
  progress: "desc",
  created: "desc",
};

/** 多选过滤。每个维度的空数组 = 未激活。 */
export interface ProjectListFilters {
  /** 项目状态值。 */
  statuses: string[];
  /** 项目优先级值。 */
  priorities: string[];
  /** 复合的 "type:id" lead 引用（member 或 agent）。 */
  leads: string[];
}

export const EMPTY_PROJECT_FILTERS: ProjectListFilters = {
  statuses: [],
  priorities: [],
  leads: [],
};

// 可隐藏的表格列。名字与状态是永远可见的核心（状态是项目最具定义性的生命
// 周期字段），所以不在这个集合里。
export type ProjectColumnKey = "priority" | "progress" | "lead" | "issues" | "created";

/** issues 计数默认隐藏（opt-in）；其余默认显示（与之前的紧凑表格一致）。 */
export const PROJECT_DEFAULT_HIDDEN_COLUMNS: ProjectColumnKey[] = ["issues"];

/** 项目列表视图的完整状态与动作。 */
export interface ProjectViewState {
  viewMode: ProjectViewMode;
  sortField: ProjectSortField;
  sortDirection: ProjectSortDirection;
  hiddenColumns: ProjectColumnKey[];
  filters: ProjectListFilters;
  setViewMode: (mode: ProjectViewMode) => void;
  toggleSort: (field: ProjectSortField) => void;
  setSortField: (field: ProjectSortField) => void;
  setSortDirection: (direction: ProjectSortDirection) => void;
  toggleColumn: (key: ProjectColumnKey) => void;
  toggleFilter: (key: keyof ProjectListFilters, value: string) => void;
  clearFilters: () => void;
}

const DEFAULTS = {
  viewMode: "compact" as ProjectViewMode,
  sortField: "created" as ProjectSortField,
  sortDirection: PROJECT_SORT_DEFAULT_DIRECTION.created,
  hiddenColumns: PROJECT_DEFAULT_HIDDEN_COLUMNS,
  filters: EMPTY_PROJECT_FILTERS,
};

/** 项目列表视图偏好 store，按工作区 slug 分桶持久化。 */
export const useProjectViewStore = create<ProjectViewState>()(
  persist(
    (set) => ({
      ...DEFAULTS,
      setViewMode: (mode) => set({ viewMode: mode }),
      toggleSort: (field) =>
        set((state) =>
          state.sortField === field
            ? { sortDirection: state.sortDirection === "asc" ? "desc" : "asc" }
            : {
                sortField: field,
                sortDirection: PROJECT_SORT_DEFAULT_DIRECTION[field],
              },
        ),
      setSortField: (field) =>
        set((state) =>
          state.sortField === field
            ? {}
            : {
                sortField: field,
                sortDirection: PROJECT_SORT_DEFAULT_DIRECTION[field],
              },
        ),
      setSortDirection: (direction) => set({ sortDirection: direction }),
      toggleColumn: (key) =>
        set((state) => ({
          hiddenColumns: state.hiddenColumns.includes(key)
            ? state.hiddenColumns.filter((k) => k !== key)
            : [...state.hiddenColumns, key],
        })),
      toggleFilter: (key, value) =>
        set((state) => {
          const list = state.filters[key] as string[];
          const next = list.includes(value)
            ? list.filter((v) => v !== value)
            : [...list, value];
          return { filters: { ...state.filters, [key]: next } };
        }),
      clearFilters: () => set({ filters: EMPTY_PROJECT_FILTERS }),
    }),
    {
      name: "multica_projects_view",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      partialize: (state) => ({
        viewMode: state.viewMode,
        sortField: state.sortField,
        sortDirection: state.sortDirection,
        hiddenColumns: state.hiddenColumns,
        filters: state.filters,
      }),
      // filters 深合并，让「早于某个过滤维度」的持久化载荷也能拿到该 key 的
      // 默认值（避免对 undefined 做 `.length`）。与其他视图 store 同样的加固。
      merge: (persisted, current) => {
        if (!persisted) return { ...current, ...DEFAULTS };
        const p = persisted as Partial<ProjectViewState>;
        return {
          ...current,
          ...p,
          filters: { ...EMPTY_PROJECT_FILTERS, ...(p.filters ?? {}) },
        };
      },
    }
  )
);

registerForWorkspaceRehydration(() => useProjectViewStore.persist.rehydrate());
