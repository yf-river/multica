import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";
import { createWorkspacePendingCreateStore } from "../platform/recoverable-operation-store";
import type { CreateAutopilotRequest } from "../types";

interface AutopilotManualTriggerState {
  manualTriggerKeys: Record<string, string>;
  setManualTriggerKey: (autopilotId: string, requestKey: string) => void;
  clearManualTriggerKey: (autopilotId: string) => void;
  clear: () => void;
}

export const useAutopilotCreateOperationStore =
  createWorkspacePendingCreateStore<CreateAutopilotRequest>(
    "multica_autopilot_create_operation",
  );

export const useAutopilotManualTriggerStore =
  create<AutopilotManualTriggerState>()(
    persist(
      (set) => ({
        manualTriggerKeys: {},
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
        clear: () => set({ manualTriggerKeys: {} }),
      }),
      {
        name: "multica_autopilot_pending_operations",
        storage: createJSONStorage(() =>
          createWorkspaceAwareStorage(defaultStorage),
        ),
        partialize: ({ manualTriggerKeys }) => ({ manualTriggerKeys }),
        merge: (persisted, current) => ({
          ...current,
          manualTriggerKeys:
            (persisted as Partial<AutopilotManualTriggerState> | undefined)
              ?.manualTriggerKeys ?? {},
        }),
      },
    ),
  );

registerWorkspacePersistStore(useAutopilotManualTriggerStore);
