import { api, type ApiClient } from "../api";
import { executeRecoverableMutation } from "../api/transport";
import type { CreateSkillRequest, Skill } from "../types";
import { generateUUID } from "../utils";
import { useSkillPendingOperationStore } from "./pending-operation-store";

type SkillCreateClient = Pick<ApiClient, "createSkill">;

export async function createSkillWithRecovery(
  request: CreateSkillRequest,
  client: SkillCreateClient = api,
): Promise<Skill> {
  const operations = useSkillPendingOperationStore.getState();
  const pendingCreate = operations.pendingCreate ?? {
    requestKey: generateUUID(),
    request,
  };
  operations.setPendingCreate(pendingCreate);
  return executeRecoverableMutation(
    () => client.createSkill(
      pendingCreate.request,
      pendingCreate.requestKey,
    ),
    () => useSkillPendingOperationStore.getState().setPendingCreate(),
  );
}
