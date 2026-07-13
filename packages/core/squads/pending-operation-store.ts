import type { CreateSquadRequest } from "../types";
import { createWorkspacePendingCreateStore } from "../platform/pending-create-store";

export const useSquadPendingOperationStore =
  createWorkspacePendingCreateStore<CreateSquadRequest>(
    "multica_squad_pending_operations",
  );
