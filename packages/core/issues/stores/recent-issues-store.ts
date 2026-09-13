"use client";

// 命令面板 / 侧边栏"最近议题"列表 store：按工作区 id 记录最近访问过的议题，
// 供 Cmd+K 等入口快速跳转。用 wsId 而不是 slug 做命名空间，避免重命名/时序问题。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../../platform/storage";

/** 单个工作区最多保留的最近议题条数。 */
const MAX_RECENT_ISSUES = 20;
/** 全部分区（工作区）数量上限，超出淘汰最久未访问者。 */
const MAX_WORKSPACES = 50;
/** 共享空数组：读取不存在的工作区时返回它，避免每次分配新数组引发重渲染。 */
const EMPTY: RecentIssueEntry[] = [];

/** 一条"最近议题"记录：议题 id + 上次访问时间戳。 */
export interface RecentIssueEntry {
  id: string;
  visitedAt: number;
}

interface RecentIssuesState {
  /** 按工作区 id 分组的最近议题列表（新访问在前）。 */
  byWorkspace: Record<string, RecentIssueEntry[]>;
  /** 记录一次访问：把议题提到该工作区列表头部。 */
  recordVisit: (wsId: string, id: string) => void;
  /** 遗忘某工作区下的某个议题（例如它已被删除）。 */
  forgetIssue: (wsId: string, id: string) => void;
  /** 只保留仍在活跃工作区集合里的分区，清理已离开的工作区数据。 */
  pruneWorkspaces: (activeWsIds: string[]) => void;
}

// 用工作区 id（UUID）做命名空间，而不是把存储 key 按 slug 命名。按存储 key 切分
// 的方案（见 createWorkspaceAwareStorage）会在子组件挂载 effect 先于
// WorkspaceRouteLayout 的 effect 设置当前 slug 时失效——子 effect 先于父 effect
// 执行，写入落到未加命名空间的裸 key 里，跨工作区泄漏（/ <slug>/issues 对其它
// 工作区的 recents 发按 id 的 GET、大多返回 404，就是这个 bug 的表象）。
//
// 按 wsId（而非 slug）做 key，数据也能扛住工作区改名，且与调用方通过
// useWorkspaceId() 拿到的 wsId 一致。
export const useRecentIssuesStore = create<RecentIssuesState>()(
  persist(
    (set) => ({
      byWorkspace: {},
      recordVisit: (wsId, id) =>
        set((state) => {
          const bucket = state.byWorkspace[wsId] ?? EMPTY;
          const filtered = bucket.filter((i) => i.id !== id);
          const updated: RecentIssueEntry = { id, visitedAt: Date.now() };
          const nextBucket = [updated, ...filtered].slice(0, MAX_RECENT_ISSUES);

          let nextByWorkspace = {
            ...state.byWorkspace,
            [wsId]: nextBucket,
          };

          // LRU 兜底：万一 pruneWorkspaces 一直没机会跑（离线、列表查询失败）而用户
          // 又碰了很多工作区，就限制总数防无界增长。逐出"最近访问最旧"的那个工作区。
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
      forgetIssue: (wsId, id) =>
        set((state) => {
          const bucket = state.byWorkspace[wsId];
          if (!bucket) return state;
          const nextBucket = bucket.filter((entry) => entry.id !== id);
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
          const next: Record<string, RecentIssueEntry[]> = {};
          for (const [wsId, items] of Object.entries(state.byWorkspace)) {
            if (allow.has(wsId)) next[wsId] = items;
            else changed = true;
          }
          return changed ? { byWorkspace: next } : state;
        }),
    }),
    {
      name: "multica_recent_issues",
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ byWorkspace: state.byWorkspace }),
      // v0 在裸 key 下存扁平 `items` 数组（若写入时工作区 slug 恰好已设置，则存在
      // `multica_recent_issues:<slug>` 下）。两种形状都不安全，因为 v0 的条目不知道
      // 自己属于哪个工作区——直接丢弃，让缓存随用户访问议题重新填充。
      version: 1,
      migrate: () => ({ byWorkspace: {} }),
    },
  ),
);

/** 选中某工作区的最近议题列表；wsId 为空或该工作区无记录时返回共享空数组。 */
export function selectRecentIssues(wsId: string | null) {
  return (state: RecentIssuesState) =>
    wsId ? (state.byWorkspace[wsId] ?? EMPTY) : EMPTY;
}
