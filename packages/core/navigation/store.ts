"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

// Paths that should not be persisted as "last visited":
//  - Auth flows (/login, /signup, /logout)
//  - Pre-workspace routes (/workspaces/new, /auth/)
//  - Pair flow (/pair/)
const EXCLUDED_PREFIXES = [
  "/login",
  "/signup",
  "/logout",
  "/workspaces/",
  "/auth/",
  "/pair/",
];

interface NavigationState {
  lastPath: string | null;
  onPathChange: (path: string) => void;
}

export const useNavigationStore = create<NavigationState>()(
  persist(
    (set) => ({
      lastPath: null,
      onPathChange: (path: string) => {
        if (!EXCLUDED_PREFIXES.some((prefix) => path.startsWith(prefix))) {
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
