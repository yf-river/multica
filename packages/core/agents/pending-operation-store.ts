import type { CreateAgentRequest } from "../types";
import {
  createWorkspacePendingCreateStore,
  type PendingCreateOperation,
} from "../platform/pending-create-store";

export type PendingAgentCreate = PendingCreateOperation<CreateAgentRequest>;

export const useAgentPendingOperationStore =
  createWorkspacePendingCreateStore<CreateAgentRequest>(
    "multica_agent_pending_operations",
  );
