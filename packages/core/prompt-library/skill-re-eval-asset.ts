"use client";

import { api, type ApiClient } from "../api";
import { executeRecoverableMutation } from "../api/transport";
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

async function execute(client: SkillReEvalAssetClient, operation: PendingSkillReEvalAsset) {
  return executeRecoverableMutation(
    () => client.preparePromptEvaluationSkillReEvalAsset(
      operation.candidateId,
      operation.request,
      operation.requestKey,
    ),
    () => useSkillReEvalAssetStore.getState().setPending(),
  );
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
