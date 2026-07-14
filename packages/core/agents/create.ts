import { api, type ApiClient } from "../api";
import { executeRecoverableMutation } from "../api/transport";
import type { Agent, CreateAgentRequest } from "../types";
import { generateUUID } from "../utils";
import { useAgentPendingOperationStore } from "./pending-operation-store";

type AgentCreateClient = Pick<ApiClient, "createAgent">;

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
  return executeRecoverableMutation(
    () => client.createAgent(
      pendingCreate.request,
      pendingCreate.requestKey,
    ),
    () => useAgentPendingOperationStore.getState().setPendingCreate(),
  );
}
