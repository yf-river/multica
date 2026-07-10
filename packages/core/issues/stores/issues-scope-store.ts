"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerWorkspacePersistStore } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

export type IssuesScope = "all" | "members" | "agents";

interface IssuesScopeState {
  scope: IssuesScope;
  setScope: (scope: IssuesScope) => void;
}

export const useIssuesScopeStore = create<IssuesScopeState>()(
  persist(
    (set) => ({
      scope: "all",
      setScope: (scope) => set({ scope }),
    }),
    {
      name: "multica_issues_scope",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    },
  ),
);

registerWorkspacePersistStore(useIssuesScopeStore);
