import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import type { CreateAgentRequest } from "../types";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

export interface PendingAgentCreate {
  requestKey: string;
  request: CreateAgentRequest;
}

interface AgentPendingOperationState {
  pendingCreate?: PendingAgentCreate;
  setPendingCreate: (pendingCreate?: PendingAgentCreate) => void;
  clear: () => void;
}

export const useAgentPendingOperationStore =
  create<AgentPendingOperationState>()(
    persist(
      (set) => ({
        pendingCreate: undefined,
        setPendingCreate: (pendingCreate) => set({ pendingCreate }),
        clear: () => set({ pendingCreate: undefined }),
      }),
      {
        name: "multica_agent_pending_operations",
        storage: createJSONStorage(() =>
          createWorkspaceAwareStorage(defaultStorage),
        ),
        partialize: ({ pendingCreate }) => ({ pendingCreate }),
      },
    ),
  );

registerWorkspacePersistStore(useAgentPendingOperationStore);
