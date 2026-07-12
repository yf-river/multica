import { api, isMutationOutcomeUnknown } from "../api";
import type { Agent, CreateAgentRequest } from "../types";
import { generateUUID } from "../utils";
import { useAgentPendingOperationStore } from "./pending-operation-store";

export interface AgentCreateClient {
  createAgent: (
    request: CreateAgentRequest,
    idempotencyKey: string,
  ) => Promise<Agent>;
}

export async function createAgentWithRecovery(
  request: CreateAgentRequest,
  client: AgentCreateClient = api,
): Promise<Agent> {
  const operations = useAgentPendingOperationStore.getState();
  const pendingCreate = operations.pendingCreate ?? {
    requestKey: generateUUID(),
    request,
  };
  operations.setPendingCreate(pendingCreate);
  try {
    const agent = await client.createAgent(
      pendingCreate.request,
      pendingCreate.requestKey,
    );
    useAgentPendingOperationStore.getState().setPendingCreate();
    return agent;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) {
      useAgentPendingOperationStore.getState().setPendingCreate();
    }
    throw error;
  }
}
