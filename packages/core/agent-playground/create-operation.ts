"use client";

import { api } from "../api";
import { executeRecoverableIntent, sameMutationRequest } from "../api/transport";
import {
  createWorkspaceRecoverableOperationStore,
  type RecoverableOperationStore,
} from "../platform/recoverable-operation-store";
import type { AgentPlaygroundDetail, CreateAgentPlaygroundExperimentRequest } from "../types";
import { generateUUID } from "../utils";

interface PendingAgentPlaygroundCreate {
  request: CreateAgentPlaygroundExperimentRequest;
  requestKey: string;
  createdAt: number;
}

const useAgentPlaygroundCreateStore: RecoverableOperationStore<PendingAgentPlaygroundCreate> =
  createWorkspaceRecoverableOperationStore<PendingAgentPlaygroundCreate>(
    "multica_agent_playground_create",
  );

export async function createAgentPlaygroundExperimentWithRecovery(
  request: CreateAgentPlaygroundExperimentRequest,
): Promise<AgentPlaygroundDetail> {
  const pending = useAgentPlaygroundCreateStore.getState().pending;
  return executeRecoverableIntent(
    pending,
    (operation) => sameMutationRequest(operation.request, request),
    () => ({ request, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => useAgentPlaygroundCreateStore.getState().setPending(operation),
    (operation) => api.createAgentPlaygroundExperiment(operation.request, operation.requestKey),
  );
}
