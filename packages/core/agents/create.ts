import { api } from "../api";
import { executePendingCreateMutation } from "../api/transport";
import { createWorkspacePendingCreateStore } from "../platform/recoverable-operation-store";
import type { Agent, CreateAgentRequest } from "../types";

const useAgentPendingOperationStore =
  createWorkspacePendingCreateStore<CreateAgentRequest>(
    "multica_agent_pending_operations",
  );

export async function createAgentWithRecovery(
  request: CreateAgentRequest,
): Promise<Agent> {
  return executePendingCreateMutation(
    useAgentPendingOperationStore,
    request,
    (operation) => api.createAgent(operation.request, operation.requestKey),
  );
}
