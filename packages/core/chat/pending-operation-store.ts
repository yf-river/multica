"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";
import { registerAccountPersistStore } from "../platform/workspace-storage";
import { api } from "../api";
import type { SendChatMessageResponse } from "../types";

/**
 * Durable client intent for one logical send. The operation id is also the
 * Idempotency-Key for session creation and message send (the server scopes the
 * two operations separately). Keeping the full intent lets a reload retry the
 * exact same request instead of asking the user to guess whether it committed.
 */
interface PendingChatOperation {
  id: string;
  accountId: string;
  workspaceId: string;
  agentId: string;
  sessionId: string | null;
  title: string;
  content: string;
  attachmentIds: string[];
  stage: "creating-session" | "sending-message";
  cancelRequested: boolean;
  createdAt: number;
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
    (set, get) => ({
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
              [id]: { ...current, ...patch },
            },
          };
        }),
      remove: (id) =>
        set((state) => {
          if (!state.operations[id]) return state;
          const { [id]: _, ...operations } = state.operations;
          return { operations };
        }),
      requestCancel: (id) => get().update(id, { cancelRequested: true }),
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

/** Execute the exact persisted intent; both requests reuse its stable UUID. */
export async function replayPendingChatOperation(
  operation: PendingChatOperation,
  onSessionCreated: (sessionId: string) => void,
): Promise<{ sessionId: string; response: SendChatMessageResponse }> {
  let sessionId = operation.sessionId;
  if (operation.stage === "creating-session") {
    const session = await api.createChatSession(
      { agent_id: operation.agentId, title: operation.title },
      operation.id,
    );
    sessionId = session.id;
    onSessionCreated(sessionId);
  }
  if (!sessionId) {
    throw new Error("pending chat send has no session id");
  }
  const response = await api.sendChatMessage(
    sessionId,
    operation.content,
    operation.id,
    operation.attachmentIds,
  );
  return { sessionId, response };
}
