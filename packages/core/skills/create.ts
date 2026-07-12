import { api, isMutationOutcomeUnknown } from "../api";
import type { CreateSkillRequest, Skill } from "../types";
import { generateUUID } from "../utils";
import { useSkillPendingOperationStore } from "./pending-operation-store";

export interface SkillCreateClient {
  createSkill: (
    request: CreateSkillRequest,
    idempotencyKey: string,
  ) => Promise<Skill>;
}

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
  try {
    const skill = await client.createSkill(
      pendingCreate.request,
      pendingCreate.requestKey,
    );
    useSkillPendingOperationStore.getState().setPendingCreate();
    return skill;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) {
      useSkillPendingOperationStore.getState().setPendingCreate();
    }
    throw error;
  }
}
