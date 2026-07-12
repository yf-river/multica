import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import type { CreateIssueRequest } from "../types";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

export interface PendingIssueCreate {
  requestKey: string;
  request: CreateIssueRequest;
}

interface IssueCreatePendingState {
  pendingCreate?: PendingIssueCreate;
  setPendingCreate: (pendingCreate?: PendingIssueCreate) => void;
}

export const useIssueCreatePendingStore = create<IssueCreatePendingState>()(
  persist(
    (set) => ({
      pendingCreate: undefined,
      setPendingCreate: (pendingCreate) => set({ pendingCreate }),
    }),
    {
      name: "multica_issue_create_pending",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      partialize: ({ pendingCreate }) => ({ pendingCreate }),
    },
  ),
);

registerWorkspacePersistStore(useIssueCreatePendingStore);
