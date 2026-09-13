"use client";

// 命令面板 / 侧边栏的"最近上下文"store：按工作区记录最近访问过的议题/项目，
// 供快速跳转（与 issues/recent-issues-store 同款分区结构）。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";
import type { IssueStatus, ProjectStatus } from "../types";

/** 单个工作区最多保留的最近上下文条数。 */
const MAX_RECENT_CONTEXTS = 20;
/** 全部分区（工作区）数量上限，超出淘汰最久未访问者。 */
const MAX_WORKSPACES = 50;
/** 共享空数组：读取不存在的工作区时返回它，避免每次分配新数组引发重渲染。 */
const EMPTY: RecentContextEntry[] = [];

/** 最近上下文的类型：议题或项目。 */
export type RecentContextType = "issue" | "project";

/** 一条最近上下文记录：标识 + 展示信息 + 上次访问时间戳。 */
export interface RecentContextEntry {
  type: RecentContextType;
  id: string;
  /** 展示用的标题。 */
  label?: string;
  /** 展示用的副标题。 */
  subtitle?: string;
  /** 议题状态（用于展示小徽标）。 */
  status?: IssueStatus;
  /** 项目状态（用于展示小徽标）。 */
  projectStatus?: ProjectStatus;
  /** 图标（emoji 或 null）。 */
  icon?: string | null;
  visitedAt: number;
}

interface RecentContextState {
  /** 按工作区 id 分组的最近上下文（新访问在前）。 */
  byWorkspace: Record<string, RecentContextEntry[]>;
  /** 记录一次访问：把该上下文提升到工作区列表头部。 */
  recordVisit: (wsId: string, entry: Pick<RecentContextEntry, "type" | "id"> & Partial<Pick<RecentContextEntry, "label" | "subtitle" | "status" | "projectStatus" | "icon">>) => void;
  /** 遗忘某工作区下的某个上下文（例如它已被删除）。 */
  forgetContext: (wsId: string, entry: Pick<RecentContextEntry, "type" | "id">) => void;
  /** 只保留仍在活跃工作区集合里的分区，清理已离开的工作区数据。 */
  pruneWorkspaces: (activeWsIds: string[]) => void;
}

/** 上下文条目的身份 key：`type:id`。 */
function entryKey(entry: Pick<RecentContextEntry, "type" | "id">): string {
  return `${entry.type}:${entry.id}`;
}

export const useRecentContextStore = create<RecentContextState>()(
  persist(
    (set) => ({
      byWorkspace: {},
      recordVisit: (wsId, entry) =>
        set((state) => {
          const bucket = state.byWorkspace[wsId] ?? EMPTY;
          const key = entryKey(entry);
          // 同一条目（同类型同 id）先去重再提到最前。
          const filtered = bucket.filter((item) => entryKey(item) !== key);
          const updated: RecentContextEntry = {
            type: entry.type,
            id: entry.id,
            label: entry.label,
            subtitle: entry.subtitle,
            status: entry.status,
            projectStatus: entry.projectStatus,
            icon: entry.icon,
            visitedAt: Date.now(),
          };
          const nextBucket = [updated, ...filtered].slice(0, MAX_RECENT_CONTEXTS);

          let nextByWorkspace = {
            ...state.byWorkspace,
            [wsId]: nextBucket,
          };

          // LRU 兜底：分区数超限时逐出"最近访问最旧"的工作区（与 recent-issues 同规则）。
          const ids = Object.keys(nextByWorkspace);
          if (ids.length > MAX_WORKSPACES) {
            const oldest = ids.reduce((oldestId, candidateId) => {
              const a = nextByWorkspace[oldestId]?.[0]?.visitedAt ?? 0;
              const b = nextByWorkspace[candidateId]?.[0]?.visitedAt ?? 0;
              return b < a ? candidateId : oldestId;
            });
            const { [oldest]: _, ...rest } = nextByWorkspace;
            nextByWorkspace = rest;
          }

          return { byWorkspace: nextByWorkspace };
        }),
      forgetContext: (wsId, entry) =>
        set((state) => {
          const bucket = state.byWorkspace[wsId];
          if (!bucket) return state;
          const key = entryKey(entry);
          const nextBucket = bucket.filter((item) => entryKey(item) !== key);
          if (nextBucket.length === bucket.length) return state;
          if (nextBucket.length === 0) {
            // 列表空了就连分区一起删，不留空壳。
            const { [wsId]: _, ...rest } = state.byWorkspace;
            return { byWorkspace: rest };
          }
          return {
            byWorkspace: { ...state.byWorkspace, [wsId]: nextBucket },
          };
        }),
      pruneWorkspaces: (activeWsIds) =>
        set((state) => {
          const allow = new Set(activeWsIds);
          let changed = false;
          const next: Record<string, RecentContextEntry[]> = {};
          for (const [wsId, items] of Object.entries(state.byWorkspace)) {
            if (allow.has(wsId)) next[wsId] = items;
            else changed = true;
          }
          return changed ? { byWorkspace: next } : state;
        }),
    }),
    {
      name: "multica_recent_contexts",
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ byWorkspace: state.byWorkspace }),
      // v0 只有一份裸 key 的扁平数据、且不带工作区归属，直接丢弃重来。
      version: 1,
      migrate: () => ({ byWorkspace: {} }),
    },
  ),
);

/** 选中某工作区的最近上下文列表；wsId 为空或无记录时返回共享空数组。 */
export function selectRecentContexts(wsId: string | null) {
  return (state: RecentContextState) =>
    wsId ? (state.byWorkspace[wsId] ?? EMPTY) : EMPTY;
}