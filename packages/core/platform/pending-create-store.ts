import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "./storage";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "./workspace-storage";

interface PendingCreateOperation<Request> {
  requestKey: string;
  request: Request;
}

export interface PendingCreateState<Request> {
  pendingCreate?: PendingCreateOperation<Request>;
  setPendingCreate: (
    pendingCreate?: PendingCreateOperation<Request>,
  ) => void;
}

export function createWorkspacePendingCreateStore<Request>(name: string) {
  const store = create<PendingCreateState<Request>>()(
    persist(
      (set) => ({
        pendingCreate: undefined,
        setPendingCreate: (pendingCreate) => set({ pendingCreate }),
      }),
      {
        name,
        storage: createJSONStorage(() =>
          createWorkspaceAwareStorage(defaultStorage),
        ),
        partialize: ({ pendingCreate }) => ({ pendingCreate }),
      },
    ),
  );

  registerWorkspacePersistStore(store);
  return store;
}
