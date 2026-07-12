import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import type { CreateSkillRequest } from "../types";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

export interface PendingSkillCreate {
  requestKey: string;
  request: CreateSkillRequest;
}

interface SkillPendingOperationState {
  pendingCreate?: PendingSkillCreate;
  setPendingCreate: (pendingCreate?: PendingSkillCreate) => void;
  clear: () => void;
}

export const useSkillPendingOperationStore =
  create<SkillPendingOperationState>()(
    persist(
      (set) => ({
        pendingCreate: undefined,
        setPendingCreate: (pendingCreate) => set({ pendingCreate }),
        clear: () => set({ pendingCreate: undefined }),
      }),
      {
        name: "multica_skill_pending_operations",
        storage: createJSONStorage(() =>
          createWorkspaceAwareStorage(defaultStorage),
        ),
        partialize: ({ pendingCreate }) => ({ pendingCreate }),
      },
    ),
  );

registerWorkspacePersistStore(useSkillPendingOperationStore);
