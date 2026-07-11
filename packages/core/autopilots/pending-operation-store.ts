import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import type { CreateAutopilotRequest } from "../types";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

interface PendingAutopilotCreate {
  requestKey: string;
  request: CreateAutopilotRequest;
}

interface AutopilotPendingOperationState {
  pendingCreate?: PendingAutopilotCreate;
  manualTriggerKeys: Record<string, string>;
  setPendingCreate: (pendingCreate?: PendingAutopilotCreate) => void;
  setManualTriggerKey: (autopilotId: string, requestKey: string) => void;
  clearManualTriggerKey: (autopilotId: string) => void;
  clear: () => void;
}

const EMPTY_OPERATIONS = {
  pendingCreate: undefined,
  manualTriggerKeys: {},
} satisfies Pick<
  AutopilotPendingOperationState,
  "pendingCreate" | "manualTriggerKeys"
>;

export const useAutopilotPendingOperationStore =
  create<AutopilotPendingOperationState>()(
    persist(
      (set) => ({
        ...EMPTY_OPERATIONS,
        setPendingCreate: (pendingCreate) => set({ pendingCreate }),
        setManualTriggerKey: (autopilotId, requestKey) =>
          set((state) => ({
            manualTriggerKeys: {
              ...state.manualTriggerKeys,
              [autopilotId]: requestKey,
            },
          })),
        clearManualTriggerKey: (autopilotId) =>
          set((state) => {
            const manualTriggerKeys = { ...state.manualTriggerKeys };
            delete manualTriggerKeys[autopilotId];
            return { manualTriggerKeys };
          }),
        clear: () => set({ ...EMPTY_OPERATIONS }),
      }),
      {
        name: "multica_autopilot_pending_operations",
        storage: createJSONStorage(() =>
          createWorkspaceAwareStorage(defaultStorage),
        ),
        partialize: ({ pendingCreate, manualTriggerKeys }) => ({
          pendingCreate,
          manualTriggerKeys,
        }),
        merge: (persisted, current) => ({
          ...current,
          ...(persisted as Partial<AutopilotPendingOperationState> | undefined),
          manualTriggerKeys:
            (persisted as Partial<AutopilotPendingOperationState> | undefined)
              ?.manualTriggerKeys ?? {},
        }),
      },
    ),
  );

registerWorkspacePersistStore(useAutopilotPendingOperationStore);
