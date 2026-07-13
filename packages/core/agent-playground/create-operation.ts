"use client";

import { api, isMutationOutcomeUnknown } from "../api";
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

export const useAgentPlaygroundCreateStore: RecoverableOperationStore<PendingAgentPlaygroundCreate> =
  createWorkspaceRecoverableOperationStore<PendingAgentPlaygroundCreate>(
    "multica_agent_playground_create",
  );

export interface AgentPlaygroundCreateClient {
  createAgentPlaygroundExperiment(
    request: CreateAgentPlaygroundExperimentRequest,
    requestKey: string,
  ): Promise<AgentPlaygroundDetail>;
}

async function execute(
  client: AgentPlaygroundCreateClient,
  operation: PendingAgentPlaygroundCreate,
) {
  try {
    const detail = await client.createAgentPlaygroundExperiment(operation.request, operation.requestKey);
    useAgentPlaygroundCreateStore.getState().setPending();
    return detail;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) {
      useAgentPlaygroundCreateStore.getState().setPending();
    }
    throw error;
  }
}

export async function createAgentPlaygroundExperimentWithRecovery(
  request: CreateAgentPlaygroundExperimentRequest,
  client: AgentPlaygroundCreateClient = api,
): Promise<AgentPlaygroundDetail> {
  const pending = useAgentPlaygroundCreateStore.getState().pending;
  if (pending) {
    const recovered = await execute(client, pending);
    if (JSON.stringify(pending.request) === JSON.stringify(request)) return recovered;
  }
  const operation = { request, requestKey: generateUUID(), createdAt: Date.now() };
  useAgentPlaygroundCreateStore.getState().setPending(operation);
  return execute(client, operation);
}
