import { api, type ApiClient } from "../api";
import { executePendingCreateMutation } from "../api/transport";
import { createWorkspacePendingCreateStore } from "../platform/recoverable-operation-store";
import type { Agent, CreateAgentRequest } from "../types";

const useAgentPendingOperationStore =
  createWorkspacePendingCreateStore<CreateAgentRequest>(
    "multica_agent_pending_operations",
  );

type AgentCreateClient = Pick<ApiClient, "createAgent">;

export async function createAgentWithRecovery(
  request: CreateAgentRequest,
  client: AgentCreateClient = api,
): Promise<Agent> {
  return executePendingCreateMutation(
    useAgentPendingOperationStore,
    request,
    (operation) => client.createAgent(operation.request, operation.requestKey),
  );
}
