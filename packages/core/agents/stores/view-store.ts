"use client";

// Agents 列表页的视图偏好 store：范围（我的/全部/已归档）、排序、列显隐、过滤。
// 按工作区、按用户/设备持久化。行选择刻意只在会话内（与 skills/autopilots 视图
// store 的理由相同）。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerForWorkspaceRehydration,
} from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";
import type { AccessScope } from "../effective-access";

// 范围混合了归属视角（mine/all）与归档生命周期阶段。纸面上不纯，但这三种在实践中
// 互斥，"mine"又是历史产品默认；归档视图完全忽略归属视角（只显示*我*归档的 Agent
// 会藏掉别人的归档 Agent，且没有任何 UI 解释为什么）。
export type AgentsScope = "mine" | "all" | "archived";

/** 全部可选的范围值（展示顺序）。 */
export const AGENT_SCOPES: AgentsScope[] = ["mine", "all", "archived"];

/** Agents 列表可用的排序字段。 */
export type AgentSortField = "lastActive" | "name" | "runs" | "created";

export type AgentSortDirection = "asc" | "desc";

/** 切到某字段时套用的默认方向。 */
export const AGENT_SORT_DEFAULT_DIRECTION: Record<
  AgentSortField,
  AgentSortDirection
> = {
  lastActive: "desc",
  name: "asc",
  runs: "desc",
  created: "desc",
};

/** 多选过滤状态。某维度为空数组 = 该维度不过滤。 */
export interface AgentListFilters {
  /** AgentAvailability 值（online / unstable / offline）。 */
  availability: string[];
  /** 运行时 id。 */
  runtimes: string[];
  /** 所有者用户 id。所有者和 Mine 范围是同一"人"轴上：Mine 是干净的无过滤个人视图，
   *  应用任一过滤（不论是否所有者）都会离开 Mine 进入 "all"——见 setScope /
   *  toggleFilter。所以"所有者当过滤"与 Mine 永不同时存在，这让两个轴保持正交
   *  （不会出现"mine + owner=别人 = 空"的状态）。 */
  owners: string[];
  /** 运行时原生的模型标识（如 claude / codex / gpt-…）。 */
  models: string[];
  /** 有效访问范围值（MUL-3963）：workspace | specific-people | owner-only。 */
  access: AccessScope[];
}

/** 全空过滤（每维都不筛）。 */
export const EMPTY_AGENT_FILTERS: AgentListFilters = {
  availability: [],
  runtimes: [],
  owners: [],
  models: [],
  access: [],
};

// 用户可隐藏的列。名称与结构列（复选框、kebab 菜单）始终可见。
export type AgentColumnKey =
  | "status"
  | "owner"
  | "access"
  | "runtime"
  | "lastActive"
  | "runs"
  | "model"
  | "created";

/** model 与 created 默认隐藏，用户开启才显示；owner 默认显示（用户想看每台归谁）。 */
export const AGENT_DEFAULT_HIDDEN_COLUMNS: AgentColumnKey[] = [
  "model",
  "created",
];

/** Agents 列表页视图状态的完整形态。 */
export interface AgentsViewState {
  scope: AgentsScope;
  sortField: AgentSortField;
  sortDirection: AgentSortDirection;
  hiddenColumns: AgentColumnKey[];
  filters: AgentListFilters;
  setScope: (scope: AgentsScope) => void;
  /** 表头点击：已在当前字段上则翻转方向，否则切到该字段并用它的默认方向。 */
  toggleSort: (field: AgentSortField) => void;
  /** 展示面板下拉：只切字段（默认方向），不翻转。 */
  setSortField: (field: AgentSortField) => void;
  setSortDirection: (direction: AgentSortDirection) => void;
  /** 开/关某列的显示。 */
  toggleColumn: (key: AgentColumnKey) => void;
  /** 开/关某过滤维度里的某个值。 */
  toggleFilter: (key: keyof AgentListFilters, value: string) => void;
  /** 清空全部过滤。 */
  clearFilters: () => void;
}

const DEFAULTS = {
  // "mine" 是历史默认——多数成员先关心自己的 Agent；管理员会切到 "all"。
  scope: "mine" as AgentsScope,
  sortField: "lastActive" as AgentSortField,
  sortDirection: AGENT_SORT_DEFAULT_DIRECTION.lastActive,
  hiddenColumns: AGENT_DEFAULT_HIDDEN_COLUMNS,
  filters: EMPTY_AGENT_FILTERS,
};

export const useAgentsViewStore = create<AgentsViewState>()(
  persist(
    (set) => ({
      ...DEFAULTS,
      // "mine" 是干净的个人视图：进入它清空所有过滤，所以 Mine 从不带过滤。
      // 切到 all/archived 保留过滤（可以在两者间携带"owner = Bob"）。
      setScope: (scope) =>
        set(scope === "mine" ? { scope, filters: EMPTY_AGENT_FILTERS } : { scope }),
      toggleSort: (field) =>
        set((state) =>
          state.sortField === field
            ? {
                sortDirection: state.sortDirection === "asc" ? "desc" : "asc",
              }
            : {
                sortField: field,
                sortDirection: AGENT_SORT_DEFAULT_DIRECTION[field],
              },
        ),
      setSortField: (field) =>
        set((state) =>
          state.sortField === field
            ? {}
            : {
                sortField: field,
                sortDirection: AGENT_SORT_DEFAULT_DIRECTION[field],
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
          // 应用任一过滤就离开干净的 "mine" 视图进 "all"——Mine 是无过滤模式
          // （见 setScope）。archived 保留自己的范围（可以携带过滤）。
          const scope = state.scope === "mine" ? "all" : state.scope;
          return { scope, filters: { ...state.filters, [key]: next } };
        }),
      clearFilters: () => set({ filters: EMPTY_AGENT_FILTERS }),
    }),
    {
      name: "multica_agents_view",
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
      // 重水合时：新工作区没有持久化值就重置为默认，而不是把上一工作区的内存视图
      // 状态留在原地。默认 merge 在 persisted 为 undefined 时保留当前状态，
      // 那会让视图状态跨工作区泄漏。
      merge: (persisted, current) => {
        if (!persisted) return { ...current, ...DEFAULTS };
        const p = persisted as Partial<AgentsViewState>;
        // 对 filters 深合并：某个过滤维度（如 `owners`）加入之前持久化的载荷，
        // 仍拿到该 key 的默认值，而不是掉成 `undefined`、在 `.length` 上崩溃。
        return {
          ...current,
          ...p,
          filters: { ...EMPTY_AGENT_FILTERS, ...(p.filters ?? {}) },
        };
      },
    },
  ),
);

// 切换工作区后重水合该工作区的 Agents 列表视图状态。
registerForWorkspaceRehydration(() => useAgentsViewStore.persist.rehydrate());
