"use client";

import { api, type ApiClient } from "../api";
import { executeRecoverableIntent, sameMutationRequest } from "../api/transport";
import {
  createWorkspaceRecoverableOperationStore,
  type RecoverableOperationStore,
} from "../platform/recoverable-operation-store";
import type { PromptEvaluationSkillReEvalRunResponse, RunPromptEvaluationSkillReEvalRequest } from "../types";
import { generateUUID } from "../utils";

interface PendingSkillReEvalRun {
  candidateId: string;
  request: RunPromptEvaluationSkillReEvalRequest;
  requestKey: string;
  createdAt: number;
}

const useSkillReEvalRunStore: RecoverableOperationStore<PendingSkillReEvalRun> =
  createWorkspaceRecoverableOperationStore<PendingSkillReEvalRun>(
    "multica_skill_re_eval_run",
  );

type SkillReEvalRunClient = Pick<ApiClient, "runPromptEvaluationSkillReEval">;

export async function runPromptEvaluationSkillReEvalWithRecovery(
  candidateId: string,
  request: RunPromptEvaluationSkillReEvalRequest,
  client: SkillReEvalRunClient = api,
): Promise<PromptEvaluationSkillReEvalRunResponse> {
  const pending = useSkillReEvalRunStore.getState().pending;
  return executeRecoverableIntent(
    pending,
    (operation) => operation.candidateId === candidateId
      && sameMutationRequest(operation.request, request),
    () => ({ candidateId, request, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => useSkillReEvalRunStore.getState().setPending(operation),
    (operation) => client.runPromptEvaluationSkillReEval(
      operation.candidateId,
      operation.request,
      operation.requestKey,
    ),
  );
}
