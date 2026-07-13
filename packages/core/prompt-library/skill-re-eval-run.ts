"use client";

import { api, isMutationOutcomeUnknown } from "../api";
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

export const useSkillReEvalRunStore: RecoverableOperationStore<PendingSkillReEvalRun> =
  createWorkspaceRecoverableOperationStore<PendingSkillReEvalRun>(
    "multica_skill_re_eval_run",
  );

export interface SkillReEvalRunClient {
  runPromptEvaluationSkillReEval(
    candidateId: string,
    request: RunPromptEvaluationSkillReEvalRequest,
    requestKey: string,
  ): Promise<PromptEvaluationSkillReEvalRunResponse>;
}

async function execute(client: SkillReEvalRunClient, operation: PendingSkillReEvalRun) {
  try {
    const response = await client.runPromptEvaluationSkillReEval(
      operation.candidateId,
      operation.request,
      operation.requestKey,
    );
    useSkillReEvalRunStore.getState().setPending();
    return response;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) useSkillReEvalRunStore.getState().setPending();
    throw error;
  }
}

export async function runPromptEvaluationSkillReEvalWithRecovery(
  candidateId: string,
  request: RunPromptEvaluationSkillReEvalRequest,
  client: SkillReEvalRunClient = api,
): Promise<PromptEvaluationSkillReEvalRunResponse> {
  const pending = useSkillReEvalRunStore.getState().pending;
  if (pending) {
    const recovered = await execute(client, pending);
    if (pending.candidateId === candidateId && JSON.stringify(pending.request) === JSON.stringify(request)) {
      return recovered;
    }
  }
  const operation = { candidateId, request, requestKey: generateUUID(), createdAt: Date.now() };
  useSkillReEvalRunStore.getState().setPending(operation);
  return execute(client, operation);
}
