"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";
import { registerAccountPersistStore } from "../platform/workspace-storage";
import type { Attachment } from "../types";

export type PendingChatOperationStage = "creating-session" | "sending-message";

/**
 * Durable client intent for one logical send. The operation id is also the
 * Idempotency-Key for session creation and message send (the server scopes the
 * two operations separately). Keeping the full intent lets a reload retry the
 * exact same request instead of asking the user to guess whether it committed.
 */
export interface PendingChatOperation {
  id: string;
  accountId: string;
  workspaceId: string;
  workspaceSlug: string;
  agentId: string;
  sourceSessionId: string | null;
  sessionId: string | null;
  title: string;
  content: string;
  attachmentIds: string[];
  attachments: Attachment[];
  stage: PendingChatOperationStage;
  cancelRequested: boolean;
  createdAt: number;
  updatedAt: number;
}

interface PendingChatOperationState {
  operations: Record<string, PendingChatOperation>;
  start: (operation: PendingChatOperation) => void;
  update: (id: string, patch: Partial<Omit<PendingChatOperation, "id" | "accountId" | "workspaceId">>) => void;
  remove: (id: string) => void;
  requestCancel: (id: string) => void;
  pruneWorkspaces: (activeWorkspaceIds: string[]) => void;
}

export const usePendingChatOperationStore = create<PendingChatOperationState>()(
  persist(
    (set) => ({
      operations: {},
      start: (operation) =>
        set((state) => ({
          operations: { ...state.operations, [operation.id]: operation },
        })),
      update: (id, patch) =>
        set((state) => {
          const current = state.operations[id];
          // Logout/reset may remove the intent while an HTTP request is still
          // resolving. A late continuation must not recreate account data.
          if (!current) return state;
          return {
            operations: {
              ...state.operations,
              [id]: { ...current, ...patch, updatedAt: Date.now() },
            },
          };
        }),
      remove: (id) =>
        set((state) => {
          if (!state.operations[id]) return state;
          const { [id]: _, ...operations } = state.operations;
          return { operations };
        }),
      requestCancel: (id) =>
        set((state) => {
          const current = state.operations[id];
          if (!current) return state;
          return {
            operations: {
              ...state.operations,
              [id]: { ...current, cancelRequested: true, updatedAt: Date.now() },
            },
          };
        }),
      pruneWorkspaces: (activeWorkspaceIds) =>
        set((state) => {
          const active = new Set(activeWorkspaceIds);
          const operations = Object.fromEntries(
            Object.entries(state.operations).filter(([, operation]) =>
              active.has(operation.workspaceId),
            ),
          );
          return Object.keys(operations).length === Object.keys(state.operations).length
            ? state
            : { operations };
        }),
    }),
    {
      name: "multica_pending_chat_operations",
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ operations: state.operations }),
      version: 1,
      migrate: () => ({ operations: {} }),
    },
  ),
);

registerAccountPersistStore(usePendingChatOperationStore);

// Attempt ownership is deliberately process-local. Persistence records what
// must be retried; this set prevents the mounted recovery effect and the click
// handler from issuing the same retry concurrently in one tab.
const inFlightOperationIds = new Set<string>();

export function claimPendingChatOperation(id: string): boolean {
  if (inFlightOperationIds.has(id)) return false;
  inFlightOperationIds.add(id);
  return true;
}

export function releasePendingChatOperation(id: string): void {
  inFlightOperationIds.delete(id);
}

export function selectPendingChatOperations(accountId: string | null, workspaceId: string | null) {
  return (state: PendingChatOperationState): PendingChatOperation[] => {
    if (!accountId || !workspaceId) return [];
    return Object.values(state.operations)
      .filter((operation) =>
        operation.accountId === accountId && operation.workspaceId === workspaceId,
      )
      .sort((a, b) => a.createdAt - b.createdAt);
  };
}
