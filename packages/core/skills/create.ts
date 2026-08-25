import { api } from "../api";
import { executePendingCreateMutation } from "../api/transport";
import { createWorkspacePendingCreateStore } from "../platform/recoverable-operation-store";
import type { CreateSkillRequest, Skill } from "../types";

const useSkillPendingOperationStore =
  createWorkspacePendingCreateStore<CreateSkillRequest>(
    "multica_skill_pending_operations",
  );

export async function createSkillWithRecovery(
  request: CreateSkillRequest,
): Promise<Skill> {
  return executePendingCreateMutation(
    useSkillPendingOperationStore,
    request,
    (operation) => api.createSkill(operation.request, operation.requestKey),
  );
}
