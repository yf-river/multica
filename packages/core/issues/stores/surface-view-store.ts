"use client";

// 各"议题视图界面"（表面）的持久化视图状态注册表：把工作区各表面（My Issues、
// 项目页、已保存视图等）各自的视图状态统一收进一份按工作区隔离的存储，
// 按需惰性创建每个表面的 vanilla store 并双向同步。
import { createStore, type StoreApi } from "zustand/vanilla";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";
import {
  type IssueViewState,
  mergeViewStatePersisted,
  viewStorePersistOptions,
  viewStoreSlice,
} from "./view-store";

/** 议题视图状态注册表的 localStorage key。 */
export const ISSUE_SURFACE_VIEW_STORAGE_KEY = "multica_issue_surface_views";

/** 持久化的视图状态形状（即 viewStorePersistOptions 的 partialize 产物）。 */
type PersistedIssueViewState = ReturnType<
  ReturnType<typeof viewStorePersistOptions>["partialize"]
>;

/** 注册表里单个表面的条目：持久化状态 + 最近更新时间。 */
interface IssueSurfaceViewEntry {
  state: PersistedIssueViewState;
  updatedAt: string;
}

interface IssueSurfaceViewRegistryState {
  /** 表面 key（如 `view:<id>`）→ 其持久化视图状态。 */
  surfaces: Record<string, IssueSurfaceViewEntry>;
  /** 把某表面的最新状态写入注册表。 */
  setSurfaceState: (surfaceKey: string, state: IssueViewState) => void;
  /** 清空某表面的注册表条目。 */
  clearSurfaceState: (surfaceKey: string) => void;
  /** 只保留仍在有效集合里的表面，清除已失效表面（如被删的保存视图）的数据。 */
  pruneSurfaceStates: (validSurfaceKeys: Iterable<string>) => void;
}

const basePersist = viewStorePersistOptions(ISSUE_SURFACE_VIEW_STORAGE_KEY);
/** 已创建的表面 store 缓存：表面 key → vanilla store。 */
const surfaceStores = new Map<string, StoreApi<IssueViewState>>();
/** 处于"从注册表重置/播种中"的表面集合：期间禁止把状态写回注册表。 */
const suppressSurfacePersist = new Set<string>();

/** 提取一份视图状态里值得持久化的子集。 */
function persistedIssueViewState(state: IssueViewState): PersistedIssueViewState {
  return basePersist.partialize(state);
}

/** 各表面视图状态的注册表 store（vanilla + persist）。 */
const issueSurfaceViewRegistryStore = createStore<IssueSurfaceViewRegistryState>()(
  persist(
    (set) => ({
      surfaces: {},
      setSurfaceState: (surfaceKey, state) =>
        set((current) => ({
          surfaces: {
            ...current.surfaces,
            [surfaceKey]: {
              state: persistedIssueViewState(state),
              updatedAt: new Date().toISOString(),
            },
          },
        })),
      clearSurfaceState: (surfaceKey) =>
        set((current) => {
          if (!current.surfaces[surfaceKey]) return current;
          const { [surfaceKey]: _removed, ...surfaces } = current.surfaces;
          return { surfaces };
        }),
      pruneSurfaceStates: (validSurfaceKeys) =>
        set((current) => {
          const valid = new Set(validSurfaceKeys);
          const surfaces = Object.fromEntries(
            Object.entries(current.surfaces).filter(([key]) => valid.has(key)),
          );
          if (Object.keys(surfaces).length === Object.keys(current.surfaces).length) {
            return current;
          }
          return { surfaces };
        }),
    }),
    {
      name: ISSUE_SURFACE_VIEW_STORAGE_KEY,
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      partialize: (state) => ({ surfaces: state.surfaces }),
      merge: (persisted, current) => {
        const p = (persisted ?? {}) as Partial<IssueSurfaceViewRegistryState>;
        return {
          ...current,
          surfaces: p.surfaces ?? {},
        };
      },
    },
  ),
);

/** 用注册表里持久化的状态重置某个表面 store（无记录则用默认值）。 */
function resetStoreFromRegistry(surfaceKey: string, store: StoreApi<IssueViewState>) {
  const persisted =
    issueSurfaceViewRegistryStore.getState().surfaces[surfaceKey]?.state;
  const defaults = viewStoreSlice(store.setState);
  // 重置期间挂起该表面的写回，防止触发无限循环。
  suppressSurfacePersist.add(surfaceKey);
  try {
    store.setState(mergeViewStatePersisted(persisted, defaults), true);
  } finally {
    suppressSurfacePersist.delete(surfaceKey);
  }
}

/** 工作区重水合完成后，把所有已创建表面 store 从注册表同步一遍。 */
function resetAllSurfaceStoresFromRegistry() {
  for (const [surfaceKey, store] of surfaceStores) {
    resetStoreFromRegistry(surfaceKey, store);
  }
}

// 切换工作区：先重水合注册表，再让所有已创建表面 store 反映新工作区的持久化状态。
registerForWorkspaceRehydration(() => {
  void Promise.resolve(issueSurfaceViewRegistryStore.persist.rehydrate()).then(
    resetAllSurfaceStoresFromRegistry,
  );
});

/**
 * 获取（必要时惰性创建）某个议题表面的 vanilla store。
 * 首次创建时用注册表里的持久化状态播种，此后订阅它的变更并写回注册表。
 * @param surfaceKey 表面身份 key（如 `view:<id>`、`issues`、`project:<id>`）。
 * @returns 该表面的 IssueViewState vanilla store。
 */
export function getIssueSurfaceViewStore(
  surfaceKey: string,
): StoreApi<IssueViewState> {
  const existing = surfaceStores.get(surfaceKey);
  if (existing) return existing;

  const store = createStore<IssueViewState>()((set) => viewStoreSlice(set));
  resetStoreFromRegistry(surfaceKey, store);
  store.subscribe((state) => {
    // 正在被外部重置/播种时跳过写回，避免把半成品状态存进去。
    if (suppressSurfacePersist.has(surfaceKey)) return;
    issueSurfaceViewRegistryStore.getState().setSurfaceState(surfaceKey, state);
  });
  surfaceStores.set(surfaceKey, store);
  return store;
}

/**
 * 已保存视图表面（`view:<id>` key）的首次打开播种：只有当该用户对这个表面还没有
 * 任何本地状态时，才应用视图存的定义。之后的打开保留用户自己的调整——那份定义只是
 * 默认值，不是同步源。
 * @param surfaceKey 已保存视图的表面 key。
 * @param definition 服务端下发的视图定义（jsonb blob）。
 */
export function seedIssueSurfaceViewState(
  surfaceKey: string,
  definition: unknown,
) {
  // 用户已有本地状态则不动——定义只是首次默认。
  const hasLocal =
    issueSurfaceViewRegistryStore.getState().surfaces[surfaceKey] !== undefined;
  if (hasLocal) return;
  const store = getIssueSurfaceViewStore(surfaceKey);
  // 视图定义是服务端持有的 jsonb blob——让它走一遍与持久化快照相同的净化器，
  // 畸形字段会退化成默认值而不是污染整个 store。setState 会触发持久化订阅，
  // 因此播种结果会和用户真实改动一样成为该表面的本地状态。
  const defaults = viewStoreSlice(store.setState);
  store.setState(mergeViewStatePersisted(definition, defaults), true);
}

/** 清空某表面的持久化视图状态（并把它重置回默认）。 */
export function clearIssueSurfaceViewState(surfaceKey: string) {
  issueSurfaceViewRegistryStore.getState().clearSurfaceState(surfaceKey);
  const store = surfaceStores.get(surfaceKey);
  if (store) resetStoreFromRegistry(surfaceKey, store);
}

/** 只保留有效表面 key 的状态（用于清理被删除的保存视图等遗留）。 */
export function pruneIssueSurfaceViewStates(validSurfaceKeys: Iterable<string>) {
  const valid = new Set(validSurfaceKeys);
  issueSurfaceViewRegistryStore.getState().pruneSurfaceStates(valid);
  for (const [surfaceKey, store] of surfaceStores) {
    if (!valid.has(surfaceKey)) resetStoreFromRegistry(surfaceKey, store);
  }
}

/** 拿一份当前注册表内容的快照（表面 key → 持久化状态条目）。 */
export function getIssueSurfaceViewStateRegistrySnapshot() {
  return issueSurfaceViewRegistryStore.getState().surfaces;
}
