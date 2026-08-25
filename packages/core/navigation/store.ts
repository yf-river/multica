"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";
import { isGlobalPath } from "../paths";

interface NavigationState {
  lastPath: string | null;
  onPathChange: (path: string) => void;
}

export const useNavigationStore = create<NavigationState>()(
  persist(
    (set) => ({
      lastPath: null,
      onPathChange: (path: string) => {
        if (!isGlobalPath(path)) {
          set({ lastPath: path });
        }
      },
    }),
    {
      name: "multica_navigation",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      partialize: (state) => ({ lastPath: state.lastPath }),
    },
  ),
);

// Workspace-aware: re-read lastPath when current workspace changes.
registerWorkspacePersistStore(useNavigationStore);
