import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "./storage";
import {
  createWorkspaceAwareStorage,
  registerAccountPersistStore,
  registerWorkspacePersistStore,
} from "./workspace-storage";

const OPERATION_TTL_MS = 30 * 24 * 60 * 60 * 1000;

export interface RecoverableOperation {
  createdAt: number;
}

interface RecoverableOperationState<Operation> {
  pending?: Operation;
  setPending: (pending?: Operation) => void;
}

function createRecoverableOperationStore<Operation extends RecoverableOperation>(
  name: string,
  workspaceScoped: boolean,
) {
  const store = create<RecoverableOperationState<Operation>>()(
    persist(
      (set) => ({ setPending: (pending) => set({ pending }) }),
      {
        name,
        storage: createJSONStorage(() =>
          workspaceScoped
            ? createWorkspaceAwareStorage(defaultStorage)
            : defaultStorage,
        ),
        partialize: ({ pending }) => ({ pending }),
        onRehydrateStorage: () => (state) => {
          if (
            state?.pending &&
            state.pending.createdAt < Date.now() - OPERATION_TTL_MS
          ) {
            state.pending = undefined;
          }
        },
      },
    ),
  );

  if (workspaceScoped) registerWorkspacePersistStore(store);
  else registerAccountPersistStore(store);
  return store;
}

export type RecoverableOperationStore<Operation extends RecoverableOperation> =
  ReturnType<typeof createRecoverableOperationStore<Operation>>;

export function createWorkspaceRecoverableOperationStore<
  Operation extends RecoverableOperation,
>(name: string): RecoverableOperationStore<Operation> {
  return createRecoverableOperationStore<Operation>(name, true);
}

export function createAccountRecoverableOperationStore<
  Operation extends RecoverableOperation,
>(name: string): RecoverableOperationStore<Operation> {
  return createRecoverableOperationStore<Operation>(name, false);
}
