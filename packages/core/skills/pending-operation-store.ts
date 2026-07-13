import type { CreateSkillRequest } from "../types";
import { createWorkspacePendingCreateStore } from "../platform/pending-create-store";

export const useSkillPendingOperationStore =
  createWorkspacePendingCreateStore<CreateSkillRequest>(
    "multica_skill_pending_operations",
  );
