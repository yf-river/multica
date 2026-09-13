// 本文件实现自动驾驶列表页的视图偏好状态（scope、排序、列显隐、筛选），按工作区持久化。

"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerForWorkspaceRehydration,
} from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

// 自动驾驶列表页的视图偏好：scope、排序、列显隐和筛选。按工作区（工作区感知存储）、
// 按用户/设备（localStorage）持久化。搜索文本和行选择刻意不存——它们是会话级的
// （和 skills 视图 store 同样的理由）。

// 状态是提升出来的 SCOPE 维度（生命周期阶段，互斥）——因此它不出现在 `filters` 里；
// 一个维度只存在一处。「all」= active + paused。没有 archived 这个 scope，因为产品
// 没有 UI 归档流程（DB 里存在该状态值但 UI 里没有任何东西能设置它）；如果以后
// 上了归档流程，再把 scope 和归档动作一起加回来。
export type AutopilotScope = "all" | "active" | "paused";

export const AUTOPILOT_SCOPES: AutopilotScope[] = ["all", "active", "paused"];

export type AutopilotSortField = "name" | "lastRun" | "nextRun" | "created";

export type AutopilotSortDirection = "asc" | "desc";

/** 切换到某个字段时应用的该字段默认排序方向。 */
export const AUTOPILOT_SORT_DEFAULT_DIRECTION: Record<
  AutopilotSortField,
  AutopilotSortDirection
> = {
  name: "asc",
  lastRun: "desc",
  nextRun: "asc",
  created: "desc",
};

/** 多选筛选状态。每个维度空数组表示未启用。 */
export interface AutopilotListFilters {
  assignees: string[];
  modes: string[];
  triggerKinds: string[];
  creators: string[];
}

export const EMPTY_AUTOPILOT_FILTERS: AutopilotListFilters = {
  assignees: [],
  modes: [],
  triggerKinds: [],
  creators: [],
};

// 用户可隐藏的列。名称列和结构列（复选框、kebab 菜单）始终可见。
export type AutopilotColumnKey =
  | "assignee"
  | "trigger"
  | "lastRun"
  | "nextRun"
  | "mode"
  | "creator"
  | "created";

/** mode、creator、created 是选择加入的：用户启用前默认隐藏。 */
export const AUTOPILOT_DEFAULT_HIDDEN_COLUMNS: AutopilotColumnKey[] = [
  "mode",
  "creator",
  "created",
];

/** 自动驾驶列表视图的状态与动作。 */
export interface AutopilotsViewState {
  scope: AutopilotScope;
  sortField: AutopilotSortField;
  sortDirection: AutopilotSortDirection;
  hiddenColumns: AutopilotColumnKey[];
  filters: AutopilotListFilters;
  setScope: (scope: AutopilotScope) => void;
  /** 表头点击：在当前字段上切换方向，否则切换到该字段并采用默认方向。 */
  toggleSort: (field: AutopilotSortField) => void;
  /** 展示面板选择：切换字段（默认方向），不做切换。 */
  setSortField: (field: AutopilotSortField) => void;
  setSortDirection: (direction: AutopilotSortDirection) => void;
  toggleColumn: (key: AutopilotColumnKey) => void;
  toggleFilter: (key: keyof AutopilotListFilters, value: string) => void;
  clearFilters: () => void;
}

const DEFAULTS = {
  scope: "all" as AutopilotScope,
  sortField: "lastRun" as AutopilotSortField,
  sortDirection: AUTOPILOT_SORT_DEFAULT_DIRECTION.lastRun,
  hiddenColumns: AUTOPILOT_DEFAULT_HIDDEN_COLUMNS,
  filters: EMPTY_AUTOPILOT_FILTERS,
};

export const useAutopilotsViewStore = create<AutopilotsViewState>()(
  persist(
    (set) => ({
      ...DEFAULTS,
      setScope: (scope) => set({ scope }),
      toggleSort: (field) =>
        set((state) =>
          state.sortField === field
            ? {
                sortDirection: state.sortDirection === "asc" ? "desc" : "asc",
              }
            : {
                sortField: field,
                sortDirection: AUTOPILOT_SORT_DEFAULT_DIRECTION[field],
              },
        ),
      setSortField: (field) =>
        set((state) =>
          state.sortField === field
            ? {}
            : {
                sortField: field,
                sortDirection: AUTOPILOT_SORT_DEFAULT_DIRECTION[field],
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
      clearFilters: () => set({ filters: EMPTY_AUTOPILOT_FILTERS }),
    }),
    {
      name: "multica_autopilots_view",
      storage: createJSONStorage(() =>
        createWorkspaceAwareStorage(defaultStorage),
      ),
      partialize: (state) => ({
        scope: state.scope,
        sortField: state.sortField,
        sortDirection: state.sortDirection,
        hiddenColumns: state.hiddenColumns,
        filters: state.filters,
      }),
      // 重新水合时，如果新工作区没有持久化值，重置为默认值，而不是保留上一个
      // 工作区的内存视图状态（和 skills 视图 store 同样的理由）。
      merge: (persisted, current) => {
        if (!persisted) return { ...current, ...DEFAULTS };
        const p = persisted as Partial<AutopilotsViewState>;
        // 深度合并 filters，这样在新的筛选维度出现之前持久化的载荷，
        // 仍能得到该 key 的默认值，而不是变成 undefined（那会让 `.length` 读取崩溃）。
        return {
          ...current,
          ...p,
          filters: { ...EMPTY_AUTOPILOT_FILTERS, ...(p.filters ?? {}) },
        };
      },
    },
  ),
);

registerForWorkspaceRehydration(() =>
  useAutopilotsViewStore.persist.rehydrate(),
);
