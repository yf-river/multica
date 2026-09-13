// 本文件实现 skills 列表页的「视图偏好」store：排序、列可见性与过滤。按工作区
// （workspace-aware 存储）、按用户/设备（localStorage）持久化。搜索文本与行
// 选中刻意不存储——它们是会话级的，持久化会让回归用户看到一份莫名被收窄的
// 列表。

"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerForWorkspaceRehydration,
} from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

export type SkillSortField = "name" | "usedBy" | "updated" | "created";

export type SkillSortDirection = "asc" | "desc";

/** 用户切到某个字段时套用的默认方向。 */
export const SKILL_SORT_DEFAULT_DIRECTION: Record<
  SkillSortField,
  SkillSortDirection
> = {
  name: "asc",
  usedBy: "desc",
  updated: "desc",
  created: "desc",
};

/** 技能的来源类型。 */
export type SkillOriginType =
  | "manual"
  | "runtime_local"
  | "clawhub"
  | "skills_sh"
  | "github";

/** 多选过滤状态。每个维度的空数组 = 未激活。 */
export interface SkillListFilters {
  usage: ("used" | "unused")[];
  origins: SkillOriginType[];
  agents: string[];
  creators: string[];
}

export const EMPTY_SKILL_FILTERS: SkillListFilters = {
  usage: [],
  origins: [],
  agents: [],
  creators: [],
};

// 用户可隐藏的列。名字与结构性列（复选框、kebab 菜单）永远可见。
export type SkillColumnKey =
  | "usedBy"
  | "source"
  | "creator"
  | "updated"
  | "created";

/** Source 和 created 默认隐藏：用户开启才显示。 */
export const DEFAULT_HIDDEN_COLUMNS: SkillColumnKey[] = ["source", "created"];

/** skills 列表页的完整视图状态与动作。 */
export interface SkillsViewState {
  sortField: SkillSortField;
  sortDirection: SkillSortDirection;
  hiddenColumns: SkillColumnKey[];
  filters: SkillListFilters;
  /** 表头点击：当前字段上切换方向；否则切到该字段并套用其默认方向。 */
  toggleSort: (field: SkillSortField) => void;
  /** 显示面板选择：切字段（默认方向），不切换。 */
  setSortField: (field: SkillSortField) => void;
  setSortDirection: (direction: SkillSortDirection) => void;
  toggleColumn: (key: SkillColumnKey) => void;
  toggleFilter: (key: keyof SkillListFilters, value: string) => void;
  clearFilters: () => void;
}

const DEFAULTS = {
  sortField: "updated" as SkillSortField,
  sortDirection: SKILL_SORT_DEFAULT_DIRECTION.updated,
  hiddenColumns: DEFAULT_HIDDEN_COLUMNS,
  filters: EMPTY_SKILL_FILTERS,
};

/** skills 列表视图偏好 store，按工作区 slug 分桶持久化。 */
export const useSkillsViewStore = create<SkillsViewState>()(
  persist(
    (set) => ({
      ...DEFAULTS,
      toggleSort: (field) =>
        set((state) =>
          state.sortField === field
            ? {
                sortDirection: state.sortDirection === "asc" ? "desc" : "asc",
              }
            : {
                sortField: field,
                sortDirection: SKILL_SORT_DEFAULT_DIRECTION[field],
              },
        ),
      setSortField: (field) =>
        set((state) =>
          state.sortField === field
            ? {}
            : {
                sortField: field,
                sortDirection: SKILL_SORT_DEFAULT_DIRECTION[field],
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
      clearFilters: () => set({ filters: EMPTY_SKILL_FILTERS }),
    }),
    {
      name: "multica_skills_view",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      partialize: (state) => ({
        sortField: state.sortField,
        sortDirection: state.sortDirection,
        hiddenColumns: state.hiddenColumns,
        filters: state.filters,
      }),
      // 水合时，若新工作区没有持久化值，就重置为默认值，而不是留下上一个
      // 工作区的内存视图状态（与 agents 视图 store 同理由）。
      merge: (persisted, current) => {
        if (!persisted) return { ...current, ...DEFAULTS };
        const p = persisted as Partial<SkillsViewState>;
        // filters 深合并，让「早于某个新过滤维度」的持久化载荷也能拿到该 key
        // 的默认值，而不是掉成 undefined（会导致 `.length` 读取崩溃）。
        return {
          ...current,
          ...p,
          filters: { ...EMPTY_SKILL_FILTERS, ...(p.filters ?? {}) },
        };
      },
    },
  ),
);

registerForWorkspaceRehydration(() => useSkillsViewStore.persist.rehydrate());
