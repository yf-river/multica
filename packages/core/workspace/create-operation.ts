"use client";

import { api, type ApiClient } from "../api";
import { executeRecoverableIntent, sameMutationRequest } from "../api/transport";
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

const useWorkspaceCreateOperationStore: RecoverableOperationStore<PendingWorkspaceCreate> =
  createAccountRecoverableOperationStore<PendingWorkspaceCreate>(
    "multica_workspace_create_operation",
  );

type WorkspaceCreateClient = Pick<ApiClient, "createWorkspace">;

export async function createWorkspaceWithRecovery(
  request: CreateWorkspaceRequest,
  client: WorkspaceCreateClient = api,
): Promise<Workspace> {
  const pending = useWorkspaceCreateOperationStore.getState().pending;
  return executeRecoverableIntent(
    pending,
    (operation) => sameMutationRequest(operation.request, request),
    () => ({ request, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => useWorkspaceCreateOperationStore.getState().setPending(operation),
    (operation) => client.createWorkspace(operation.request, operation.requestKey),
  );
}
