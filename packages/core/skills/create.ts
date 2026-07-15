import { api, type ApiClient } from "../api";
import { executePendingMutation } from "../api/transport";
import { createWorkspacePendingCreateStore } from "../platform/pending-create-store";
import type { CreateSkillRequest, Skill } from "../types";
import { generateUUID } from "../utils";

const useSkillPendingOperationStore =
  createWorkspacePendingCreateStore<CreateSkillRequest>(
    "multica_skill_pending_operations",
  );

type SkillCreateClient = Pick<ApiClient, "createSkill">;

export async function createSkillWithRecovery(
  request: CreateSkillRequest,
  client: SkillCreateClient = api,
): Promise<Skill> {
  const operations = useSkillPendingOperationStore.getState();
  return executePendingMutation(
    operations.pendingCreate,
    () => ({ requestKey: generateUUID(), request }),
    operations.setPendingCreate,
    (operation) => client.createSkill(operation.request, operation.requestKey),
  );
}
