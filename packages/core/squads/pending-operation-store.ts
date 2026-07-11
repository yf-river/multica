import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import type { CreateSquadRequest } from "../types";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

interface PendingSquadCreate {
  requestKey: string;
  request: CreateSquadRequest;
}

interface SquadPendingOperationState {
  pendingCreate?: PendingSquadCreate;
  setPendingCreate: (pendingCreate?: PendingSquadCreate) => void;
  clear: () => void;
}

export const useSquadPendingOperationStore =
  create<SquadPendingOperationState>()(
    persist(
      (set) => ({
        pendingCreate: undefined,
        setPendingCreate: (pendingCreate) => set({ pendingCreate }),
        clear: () => set({ pendingCreate: undefined }),
      }),
      {
        name: "multica_squad_pending_operations",
        storage: createJSONStorage(() =>
          createWorkspaceAwareStorage(defaultStorage),
        ),
        partialize: ({ pendingCreate }) => ({ pendingCreate }),
      },
    ),
  );

registerWorkspacePersistStore(useSquadPendingOperationStore);
