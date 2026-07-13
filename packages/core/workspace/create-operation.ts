"use client";

import { api, isMutationOutcomeUnknown } from "../api";
import {
  createAccountRecoverableOperationStore,
  type RecoverableOperationStore,
} from "../platform/recoverable-operation-store";
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

export const useWorkspaceCreateOperationStore: RecoverableOperationStore<PendingWorkspaceCreate> =
  createAccountRecoverableOperationStore<PendingWorkspaceCreate>(
    "multica_workspace_create_operation",
  );

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
