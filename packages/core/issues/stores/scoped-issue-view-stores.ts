"use client";

import { createStore, type StoreApi } from "zustand/vanilla";
import { persist } from "zustand/middleware";
import { registerWorkspacePersistStore } from "../../platform/workspace-storage";
import {
  type IssueViewState,
  viewStorePersistOptions,
  viewStoreSlice,
} from "./view-store";

type ScopedIssueViewState<Scope extends string> = IssueViewState & {
  scope: Scope;
  setScope: (scope: Scope) => void;
};

function createScopedIssueViewStore<Scope extends string>(
  persistKey: string,
  initialScope: Scope,
  initialViewMode?: IssueViewState["viewMode"],
): StoreApi<ScopedIssueViewState<Scope>> {
  const basePersist = viewStorePersistOptions(persistKey);
  const store = createStore<ScopedIssueViewState<Scope>>()(
    persist(
      (set) => ({
        ...viewStoreSlice(set as unknown as StoreApi<IssueViewState>["setState"]),
        ...(initialViewMode ? { viewMode: initialViewMode } : {}),
        scope: initialScope,
        setScope: (scope) => set({ scope }),
      }),
      {
        name: basePersist.name,
        storage: basePersist.storage,
        partialize: (state) => ({
          ...basePersist.partialize(state),
          scope: state.scope,
        }),
      },
    ),
  );
  registerWorkspacePersistStore(store);
  return store;
}

export type ActorIssuesScope = "assigned" | "created";
export const actorIssuesViewStore = createScopedIssueViewStore<ActorIssuesScope>(
  "multica_actor_issues_view",
  "assigned" as ActorIssuesScope,
  "list",
);

export type MyIssuesScope = "all" | "assigned" | "created" | "agents";
export const myIssuesViewStore = createScopedIssueViewStore<MyIssuesScope>(
  "multica_my_issues_view",
  "assigned" as MyIssuesScope,
);
