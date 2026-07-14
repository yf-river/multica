"use client";

import { api, type ApiClient } from "../api";
import { executeRecoverableIntent, sameMutationRequest } from "../api/transport";
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

const useSkillReEvalAssetStore: RecoverableOperationStore<PendingSkillReEvalAsset> =
  createWorkspaceRecoverableOperationStore<PendingSkillReEvalAsset>(
    "multica_skill_re_eval_asset",
  );

type SkillReEvalAssetClient = Pick<
  ApiClient,
  "preparePromptEvaluationSkillReEvalAsset"
>;

export async function preparePromptEvaluationSkillReEvalAssetWithRecovery(
  candidateId: string,
  request: PreparePromptEvaluationSkillReEvalRequest,
  client: SkillReEvalAssetClient = api,
): Promise<PromptEvaluationSkillReEvalAssetResponse> {
  const pending = useSkillReEvalAssetStore.getState().pending;
  return executeRecoverableIntent(
    pending,
    (operation) => operation.candidateId === candidateId
      && sameMutationRequest(operation.request, request),
    () => ({ candidateId, request, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => useSkillReEvalAssetStore.getState().setPending(operation),
    (operation) => client.preparePromptEvaluationSkillReEvalAsset(
      operation.candidateId,
      operation.request,
      operation.requestKey,
    ),
  );
}
