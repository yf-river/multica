import { create } from "zustand";

/**
 * Controls the full-window workspace-creation flow on desktop.
 *
 * Workspace creation is not a tab route: persisting it in the tab system
 * would leave dashboard chrome visible and could restore a transition flow as
 * a normal tab. The navigation adapter opens this overlay for
 * `/workspaces/new`.
 */
interface WorkspaceCreationOverlayStore {
  isOpen: boolean;
  open: () => void;
  close: () => void;
}

export const useWorkspaceCreationOverlayStore =
  create<WorkspaceCreationOverlayStore>((set) => ({
    isOpen: false,
    open: () => set({ isOpen: true }),
    close: () => set({ isOpen: false }),
  }));
