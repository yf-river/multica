"use client";

// "某 actor（成员/Agent）的议题"侧边面板的视图状态 store：
// 记录当前看的是"分配给他"还是"他创建"的议题，并随工作区切换做持久化重水合。
import { createStore, type StoreApi } from "zustand/vanilla";
import { persist } from "zustand/middleware";
import {
  type IssueViewState,
  viewStoreSlice,
  viewStorePersistOptions,
  mergeViewStatePersisted,
} from "./view-store";
import { registerForWorkspaceRehydration } from "../../platform/workspace-storage";

/** actor 议题面板的两种关系作用域：指派给他的、或他创建的。 */
export type ActorIssuesScope = "assigned" | "created";

/** actor 议题面板的视图状态：通用视图字段 + 关系作用域。 */
export interface ActorIssuesViewState extends IssueViewState {
  scope: ActorIssuesScope;
  setScope: (scope: ActorIssuesScope) => void;
}

const basePersist = viewStorePersistOptions("multica_actor_issues_view");

const _actorIssuesViewStore = createStore<ActorIssuesViewState>()(
  persist(
    (set) => ({
      ...viewStoreSlice(set as unknown as StoreApi<IssueViewState>["setState"]),
      // Actor 议题面板只支持列表形态；把切片默认的看板（board）改成列表。
      viewMode: "list",
      scope: "assigned" as ActorIssuesScope,
      setScope: (scope: ActorIssuesScope) => set({ scope }),
    }),
    {
      name: basePersist.name,
      storage: basePersist.storage,
      partialize: (state: ActorIssuesViewState) => ({
        ...basePersist.partialize(state),
        scope: state.scope,
      }),
      merge: mergeViewStatePersisted<ActorIssuesViewState>,
    },
  ),
);

/** actor 议题面板的 vanilla store 实例。 */
export const actorIssuesViewStore: StoreApi<ActorIssuesViewState> =
  _actorIssuesViewStore;

// 切换工作区后把持久化的视图状态重新水合回来（不同工作区各存一份）。
registerForWorkspaceRehydration(() =>
  _actorIssuesViewStore.persist.rehydrate(),
);
