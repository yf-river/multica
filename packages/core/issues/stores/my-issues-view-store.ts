"use client";

// "我的议题"页面的视图状态 store（vanilla）：记录当前选中的关系范围标签，
// 随工作区切换持久化并重水合。
import { createStore, type StoreApi } from "zustand/vanilla";
import { persist } from "zustand/middleware";
import {
  type IssueViewState,
  viewStoreSlice,
  viewStorePersistOptions,
  mergeViewStatePersisted,
} from "./view-store";
import { registerForWorkspaceRehydration } from "../../platform/workspace-storage";

/** "我的议题"页面上层展示用的范围标签。 */
export type MyIssuesScope = "all" | "assigned" | "created" | "agents";
/** 议题接口认识的关系枚举（"agents" 对应底层 "involved"）。 */
export type MyIssuesRelation = "all" | "assigned" | "created" | "involved";

// "agents" 是面向用户的展示标签；议题 API 把同样的归属/小组关系叫 "involved"。
// 这两个命名之间需要翻译。
export function myIssuesRelationFromScope(
  scope: MyIssuesScope,
): MyIssuesRelation {
  if (scope === "agents") return "involved";
  return scope;
}

/** "我的议题"视图状态：通用视图字段 + 关系范围标签。 */
export interface MyIssuesViewState extends IssueViewState {
  scope: MyIssuesScope;
  setScope: (scope: MyIssuesScope) => void;
}

const basePersist = viewStorePersistOptions("multica_my_issues_view");

const _myIssuesViewStore = createStore<MyIssuesViewState>()(
  persist(
    (set) => ({
      ...viewStoreSlice(set as unknown as StoreApi<IssueViewState>["setState"]),
      scope: "assigned" as MyIssuesScope,
      setScope: (scope: MyIssuesScope) => set({ scope }),
    }),
    {
      name: basePersist.name,
      storage: basePersist.storage,
      partialize: (state: MyIssuesViewState) => ({
        ...basePersist.partialize(state),
        scope: state.scope,
      }),
      // 复用基础视图 store 的深合并：新加的 cardProperties 开关要为存量用户
      // 继承默认值。不做这步的话，老用户持久化的快照早于 `labels` key，
      // 浅合并胜出，我的议题页会渲染不出标签。
      merge: mergeViewStatePersisted<MyIssuesViewState>,
    },
  ),
);

/** "我的议题"视图的 vanilla store 实例。 */
export const myIssuesViewStore: StoreApi<MyIssuesViewState> = _myIssuesViewStore;

// 切换工作区后重水合该工作区的视图状态。
registerForWorkspaceRehydration(() => _myIssuesViewStore.persist.rehydrate());
