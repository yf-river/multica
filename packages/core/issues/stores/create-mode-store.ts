"use client";

import { create } from "zustand";
import { useModalStore } from "../../modals";

/**
 * Kept as a public type because CreateIssueDialog still accepts an initial
 * mode prop for legacy modal keys. Product-wise, new issue creation is now a
 * single agent-create flow; manual mode is not a persisted preference.
 */
export type CreateMode = "agent" | "manual";

interface CreateModeState {
  lastMode: CreateMode;
  setLastMode: (mode: CreateMode) => void;
}

export const useCreateModeStore = create<CreateModeState>()((set) => ({
  lastMode: "agent",
  setLastMode: (mode) => set({ lastMode: mode }),
}));

/**
 * Open the single create-issue flow. Generic entry points always start in
 * agent-create; legacy callers can still pass seed data such as project_id,
 * status, assignee, due_date, or parent_issue_id.
 */
export function openCreateIssueWithPreference(
  data?: Record<string, unknown> | null,
) {
  useModalStore.getState().open("quick-create-issue", data ?? null);
}
