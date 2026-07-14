import { api, type ApiClient } from "../api";
import { executePendingMutation } from "../api/transport";
import type { Agent, CreateAgentRequest } from "../types";
import { generateUUID } from "../utils";
import { useAgentPendingOperationStore } from "./pending-operation-store";

type AgentCreateClient = Pick<ApiClient, "createAgent">;

export async function createAgentWithRecovery(
  request: CreateAgentRequest,
  client: AgentCreateClient = api,
): Promise<Agent> {
  const operations = useAgentPendingOperationStore.getState();
  return executePendingMutation(
    operations.pendingCreate,
    () => ({ requestKey: generateUUID(), request }),
    operations.setPendingCreate,
    (operation) => client.createAgent(operation.request, operation.requestKey),
  );
}
