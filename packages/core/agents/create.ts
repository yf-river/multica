import { api, type ApiClient } from "../api";
import { executePendingMutation } from "../api/transport";
import { createWorkspacePendingCreateStore } from "../platform/pending-create-store";
import type { Agent, CreateAgentRequest } from "../types";
import { generateUUID } from "../utils";

const useAgentPendingOperationStore =
  createWorkspacePendingCreateStore<CreateAgentRequest>(
    "multica_agent_pending_operations",
  );

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
