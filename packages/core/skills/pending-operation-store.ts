import type { CreateSkillRequest } from "../types";
import {
  createWorkspacePendingCreateStore,
  type PendingCreateOperation,
} from "../platform/pending-create-store";

export type PendingSkillCreate = PendingCreateOperation<CreateSkillRequest>;

export const useSkillPendingOperationStore =
  createWorkspacePendingCreateStore<CreateSkillRequest>(
    "multica_skill_pending_operations",
  );
