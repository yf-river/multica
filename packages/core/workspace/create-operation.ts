"use client";

import { api, type ApiClient } from "../api";
import { executeRecoverableMutation } from "../api/transport";
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

type WorkspaceCreateClient = Pick<ApiClient, "createWorkspace">;

async function execute(client: WorkspaceCreateClient, operation: PendingWorkspaceCreate) {
  return executeRecoverableMutation(
    () => client.createWorkspace(operation.request, operation.requestKey),
    () => useWorkspaceCreateOperationStore.getState().setPending(),
  );
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
