import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "./storage";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "./workspace-storage";

export interface PendingCreateOperation<Request> {
  requestKey: string;
  request: Request;
}

export interface PendingCreateState<Request> {
  pendingCreate?: PendingCreateOperation<Request>;
  setPendingCreate: (
    pendingCreate?: PendingCreateOperation<Request>,
  ) => void;
  clear: () => void;
}

export function createWorkspacePendingCreateStore<Request>(name: string) {
  const store = create<PendingCreateState<Request>>()(
    persist(
      (set) => ({
        pendingCreate: undefined,
        setPendingCreate: (pendingCreate) => set({ pendingCreate }),
        clear: () => set({ pendingCreate: undefined }),
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
