"use client";

import { api, isMutationOutcomeUnknown } from "../api";
import {
  createWorkspaceRecoverableOperationStore,
  type RecoverableOperationStore,
} from "../platform/recoverable-operation-store";
import type {
  PreparePromptEvaluationSkillReEvalRequest,
  PromptEvaluationSkillReEvalAssetResponse,
} from "../types";
import { generateUUID } from "../utils";

interface PendingSkillReEvalAsset {
  candidateId: string;
  request: PreparePromptEvaluationSkillReEvalRequest;
  requestKey: string;
  createdAt: number;
}

export const useSkillReEvalAssetStore: RecoverableOperationStore<PendingSkillReEvalAsset> =
  createWorkspaceRecoverableOperationStore<PendingSkillReEvalAsset>(
    "multica_skill_re_eval_asset",
  );

export interface SkillReEvalAssetClient {
  preparePromptEvaluationSkillReEvalAsset(
    candidateId: string,
    request: PreparePromptEvaluationSkillReEvalRequest,
    requestKey: string,
  ): Promise<PromptEvaluationSkillReEvalAssetResponse>;
}

async function execute(client: SkillReEvalAssetClient, operation: PendingSkillReEvalAsset) {
  try {
    const response = await client.preparePromptEvaluationSkillReEvalAsset(
      operation.candidateId,
      operation.request,
      operation.requestKey,
    );
    useSkillReEvalAssetStore.getState().setPending();
    return response;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) useSkillReEvalAssetStore.getState().setPending();
    throw error;
  }
}

export async function preparePromptEvaluationSkillReEvalAssetWithRecovery(
  candidateId: string,
  request: PreparePromptEvaluationSkillReEvalRequest,
  client: SkillReEvalAssetClient = api,
): Promise<PromptEvaluationSkillReEvalAssetResponse> {
  const pending = useSkillReEvalAssetStore.getState().pending;
  if (pending) {
    const recovered = await execute(client, pending);
    if (pending.candidateId === candidateId && JSON.stringify(pending.request) === JSON.stringify(request)) {
      return recovered;
    }
  }
  const operation = { candidateId, request, requestKey: generateUUID(), createdAt: Date.now() };
  useSkillReEvalAssetStore.getState().setPending(operation);
  return execute(client, operation);
}
