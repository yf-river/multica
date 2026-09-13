// 本文件实现 squads 列表页的「视图偏好」store：范围、排序、列可见性与多选
// 过滤。按工作区、按用户/设备持久化。没有过滤条件（集合很小）、没有搜索
// （这是带范围的列表）。与 agents/skills 的视图 store 结构一致。

"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerForWorkspaceRehydration,
} from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

// squads 列表页的视图偏好：范围、排序、列可见性。
// 按工作区、按用户/设备持久化。无过滤条件（集合很小）；
// 无搜索（带范围的列表）。镜像 agents/skills 的视图 store。

// 范围是「归属」视角（基于创建者）。没有 "archived" 范围：列表接口硬过滤掉
// 已归档小队，而且没有恢复接口，所以归档小队无法被展示或管理。
export type SquadsScope = "mine" | "all";

export const SQUAD_SCOPES: SquadsScope[] = ["mine", "all"];

export type SquadSortField = "name" | "members" | "created";

export type SquadSortDirection = "asc" | "desc";

/** 用户切到某个字段时套用的默认方向。 */
export const SQUAD_SORT_DEFAULT_DIRECTION: Record<
  SquadSortField,
  SquadSortDirection
> = {
  name: "asc",
  members: "desc",
  created: "desc",
};

// 用户可隐藏的列。名字和 leader（小队最具定义性的关系）永远可见。
export type SquadColumnKey = "members" | "creator" | "created";

/** 创建（日期）列默认隐藏。Creator（"Created by"）默认显示——用户想看每个
 *  小队是谁建的。注意它是 "Created by" 而不是 "Owner"：小队创建者不持有任何
 *  管理权（归档只有工作区 admin 能做），标成 Owner 会误导。 */
export const SQUAD_DEFAULT_HIDDEN_COLUMNS: SquadColumnKey[] = ["created"];

/** 多选过滤——分类列（leader、creator）。每个维度的空数组 = 未激活。 */
export interface SquadListFilters {
  /** Leader（小队队长）的 agent id。 */
  leaders: string[];
  /** Creator（创建者）的成员用户 id。 */
  creators: string[];
}

export const EMPTY_SQUAD_FILTERS: SquadListFilters = {
  leaders: [],
  creators: [],
};

/** squads 列表页的完整视图状态与动作。 */
export interface SquadsViewState {
  scope: SquadsScope;
  sortField: SquadSortField;
  sortDirection: SquadSortDirection;
  hiddenColumns: SquadColumnKey[];
  filters: SquadListFilters;
  setScope: (scope: SquadsScope) => void;
  /** 表头点击：当前字段上切换方向；否则切到该字段并套用其默认方向。 */
  toggleSort: (field: SquadSortField) => void;
  /** 显示面板选择：切字段（默认方向），不切换。 */
  setSortField: (field: SquadSortField) => void;
  setSortDirection: (direction: SquadSortDirection) => void;
  toggleColumn: (key: SquadColumnKey) => void;
  toggleFilter: (key: keyof SquadListFilters, value: string) => void;
  clearFilters: () => void;
}

const DEFAULTS = {
  scope: "mine" as SquadsScope,
  sortField: "name" as SquadSortField,
  sortDirection: SQUAD_SORT_DEFAULT_DIRECTION.name,
  hiddenColumns: SQUAD_DEFAULT_HIDDEN_COLUMNS,
  filters: EMPTY_SQUAD_FILTERS,
};

/** squads 列表视图偏好 store，按工作区 slug 分桶持久化。 */
export const useSquadsViewStore = create<SquadsViewState>()(
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
                sortDirection: SQUAD_SORT_DEFAULT_DIRECTION[field],
              },
        ),
      setSortField: (field) =>
        set((state) =>
          state.sortField === field
            ? {}
            : {
                sortField: field,
                sortDirection: SQUAD_SORT_DEFAULT_DIRECTION[field],
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
      clearFilters: () => set({ filters: EMPTY_SQUAD_FILTERS }),
    }),
    {
      name: "multica_squads_view",
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
      // 水合时，若新工作区没有持久化值，就重置为默认值，而不是泄漏上一个
      // 工作区的状态。filters 深合并，让「还没有 filters 字段」的旧载荷也能
      // 补上默认值。
      merge: (persisted, current) => {
        if (!persisted) return { ...current, ...DEFAULTS };
        const p = persisted as Partial<SquadsViewState>;
        return {
          ...current,
          ...p,
          filters: { ...EMPTY_SQUAD_FILTERS, ...(p.filters ?? {}) },
        };
      },
    },
  ),
);

registerForWorkspaceRehydration(() => useSquadsViewStore.persist.rehydrate());
