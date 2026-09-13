"use client";

// 把某个议题视图 store（StoreApi）经由 React Context 提供给组件树：
// 提供 Provider + 两个取值 hook。用于保存视图等需要按视图实例提供 store 的场景。
import { createContext, use } from "react";
import { useStore, type StoreApi } from "zustand";
import type { IssueViewState } from "./view-store";

/** 视图 store 的 Context（默认空，Provider 负责提供）。 */
const ViewStoreContext = createContext<StoreApi<IssueViewState> | null>(null);

/**
 * 把给定的视图 store 注入组件树。
 * @param store 要提供的 IssueViewState store 实例。
 */
export function ViewStoreProvider({
  store,
  children,
}: {
  store: StoreApi<IssueViewState>;
  children: React.ReactNode;
}) {
  return (
    <ViewStoreContext.Provider value={store}>
      {children}
    </ViewStoreContext.Provider>
  );
}

/**
 * 从 Context 里的视图 store 按 selector 取值的 hook（带订阅，值变才重渲染）。
 * @param selector 从视图状态中选取需要的片段。
 * @returns selector 选出的值。
 * @throws 在 ViewStoreProvider 之外使用时报错。
 */
export function useViewStore<T>(selector: (state: IssueViewState) => T): T {
  const store = use(ViewStoreContext);
  if (!store)
    throw new Error("useViewStore must be used within ViewStoreProvider");
  return useStore(store, selector);
}

/**
 * 拿到 Context 里的 store 本体（用于不订阅、直接调用方法/取瞬时值的场景）。
 * @throws 在 ViewStoreProvider 之外使用时报错。
 */
export function useViewStoreApi(): StoreApi<IssueViewState> {
  const store = use(ViewStoreContext);
  if (!store)
    throw new Error("useViewStoreApi must be used within ViewStoreProvider");
  return store;
}
