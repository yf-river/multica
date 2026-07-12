"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { api, isMutationOutcomeUnknown } from "../api";
import { defaultStorage } from "../platform/storage";
import { registerAccountPersistStore } from "../platform/workspace-storage";
import type { Workspace } from "../types";
import { generateUUID } from "../utils";

export interface CreateWorkspaceRequest {
  name: string;
  slug: string;
  description?: string;
  context?: string;
}

interface PendingWorkspaceCreate {
  request: CreateWorkspaceRequest;
  requestKey: string;
  createdAt: number;
}

interface WorkspaceCreateOperationState {
  pending?: PendingWorkspaceCreate;
  setPending: (pending?: PendingWorkspaceCreate) => void;
}

export const useWorkspaceCreateOperationStore = create<WorkspaceCreateOperationState>()(
  persist(
    (set) => ({
      setPending: (pending) => set({ pending }),
    }),
    {
      name: "multica_workspace_create_operation",
      storage: createJSONStorage(() => defaultStorage),
      partialize: ({ pending }) => ({ pending }),
      onRehydrateStorage: () => (state) => {
        if (state?.pending && state.pending.createdAt < Date.now() - 30 * 24 * 60 * 60 * 1000) {
          state.pending = undefined;
        }
      },
    },
  ),
);

registerAccountPersistStore(useWorkspaceCreateOperationStore);

export interface WorkspaceCreateClient {
  createWorkspace(request: CreateWorkspaceRequest, requestKey: string): Promise<Workspace>;
}

async function execute(client: WorkspaceCreateClient, operation: PendingWorkspaceCreate) {
  try {
    const workspace = await client.createWorkspace(operation.request, operation.requestKey);
    useWorkspaceCreateOperationStore.getState().setPending();
    return workspace;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) {
      useWorkspaceCreateOperationStore.getState().setPending();
    }
    throw error;
  }
}

export async function createWorkspaceWithRecovery(
  request: CreateWorkspaceRequest,
  client: WorkspaceCreateClient = api,
): Promise<Workspace> {
  const pending = useWorkspaceCreateOperationStore.getState().pending;
  if (pending) {
    const recovered = await execute(client, pending);
    if (JSON.stringify(pending.request) === JSON.stringify(request)) return recovered;
  }
  const operation = { request, requestKey: generateUUID(), createdAt: Date.now() };
  useWorkspaceCreateOperationStore.getState().setPending(operation);
  return execute(client, operation);
}
