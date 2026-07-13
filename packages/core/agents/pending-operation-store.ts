import type { CreateAgentRequest } from "../types";
import { createWorkspacePendingCreateStore } from "../platform/pending-create-store";

export const useAgentPendingOperationStore =
  createWorkspacePendingCreateStore<CreateAgentRequest>(
    "multica_agent_pending_operations",
  );
