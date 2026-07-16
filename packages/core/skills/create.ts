import { api, type ApiClient } from "../api";
import { executePendingCreateMutation } from "../api/transport";
import { createWorkspacePendingCreateStore } from "../platform/recoverable-operation-store";
import type { CreateSkillRequest, Skill } from "../types";

const useSkillPendingOperationStore =
  createWorkspacePendingCreateStore<CreateSkillRequest>(
    "multica_skill_pending_operations",
  );

type SkillCreateClient = Pick<ApiClient, "createSkill">;

export async function createSkillWithRecovery(
  request: CreateSkillRequest,
  client: SkillCreateClient = api,
): Promise<Skill> {
  return executePendingCreateMutation(
    useSkillPendingOperationStore,
    request,
    (operation) => client.createSkill(operation.request, operation.requestKey),
  );
}
