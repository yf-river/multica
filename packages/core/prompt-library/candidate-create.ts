"use client";

import { api, type ApiClient } from "../api";
import { executeRecoverableIntent } from "../api/transport";
import {
  createWorkspaceRecoverableOperationStore,
  type RecoverableOperationStore,
} from "../platform/recoverable-operation-store";
import type { PromptEvaluationOptimizationCandidate } from "../types";
import { generateUUID } from "../utils";

interface PendingCandidateCreate {
  runId: string;
  requestKey: string;
  createdAt: number;
}

const useCandidateCreateStore: RecoverableOperationStore<PendingCandidateCreate> =
  createWorkspaceRecoverableOperationStore<PendingCandidateCreate>(
    "multica_prompt_candidate_create",
  );

type CandidateCreateClient = Pick<
  ApiClient,
  "createPromptEvaluationOptimizationCandidate"
>;

export async function createPromptEvaluationOptimizationCandidateWithRecovery(
  runId: string,
  client: CandidateCreateClient = api,
): Promise<PromptEvaluationOptimizationCandidate> {
  const pending = useCandidateCreateStore.getState().pending;
  return executeRecoverableIntent(
    pending,
    (operation) => operation.runId === runId,
    () => ({ runId, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => useCandidateCreateStore.getState().setPending(operation),
    (operation) => client.createPromptEvaluationOptimizationCandidate(
      operation.runId,
      operation.requestKey,
    ),
  );
}
