"use client";

import { api, type ApiClient } from "../api";
import { executeRecoverableIntent, sameMutationRequest } from "../api/transport";
import {
  createWorkspaceRecoverableOperationStore,
  type RecoverableOperationStore,
} from "../platform/recoverable-operation-store";
import type {
  PreparePromptEvaluationSkillReEvalRequest,
  PromptEvaluationRun,
  RunPromptEvaluationSkillReEvalRequest,
} from "../types";
import { generateUUID } from "../utils";

interface PendingSkillReEval<Request> {
  candidateId: string;
  request: Request;
  requestKey: string;
  createdAt: number;
}

const assetStore = createWorkspaceRecoverableOperationStore<
  PendingSkillReEval<PreparePromptEvaluationSkillReEvalRequest>
>("multica_skill_re_eval_asset");
const runStore = createWorkspaceRecoverableOperationStore<
  PendingSkillReEval<RunPromptEvaluationSkillReEvalRequest>
>("multica_skill_re_eval_run");

function executeSkillReEval<Request, Response>(
  candidateId: string,
  request: Request,
  store: RecoverableOperationStore<PendingSkillReEval<Request>>,
  execute: (operation: PendingSkillReEval<Request>) => Promise<Response>,
) {
  return executeRecoverableIntent(
    store.getState().pending,
    (operation) => operation.candidateId === candidateId
      && sameMutationRequest(operation.request, request),
    () => ({ candidateId, request, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => store.getState().setPending(operation),
    execute,
  );
}

type SkillReEvalAssetClient = Pick<ApiClient, "preparePromptEvaluationSkillReEvalAsset">;

export function preparePromptEvaluationSkillReEvalAssetWithRecovery(
  candidateId: string,
  request: PreparePromptEvaluationSkillReEvalRequest,
  client: SkillReEvalAssetClient = api,
): Promise<{ assetId: string; caseCount: number }> {
  return executeSkillReEval(candidateId, request, assetStore, (operation) =>
    client.preparePromptEvaluationSkillReEvalAsset(
      operation.candidateId,
      operation.request,
      operation.requestKey,
    ));
}

type SkillReEvalRunClient = Pick<ApiClient, "runPromptEvaluationSkillReEval">;

export function runPromptEvaluationSkillReEvalWithRecovery(
  candidateId: string,
  request: RunPromptEvaluationSkillReEvalRequest,
  client: SkillReEvalRunClient = api,
): Promise<PromptEvaluationRun["status"]> {
  return executeSkillReEval(candidateId, request, runStore, (operation) =>
    client.runPromptEvaluationSkillReEval(
      operation.candidateId,
      operation.request,
      operation.requestKey,
    ));
}
