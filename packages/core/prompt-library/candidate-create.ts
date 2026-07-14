"use client";

import { api, type ApiClient } from "../api";
import { executeRecoverableMutation } from "../api/transport";
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

export const useCandidateCreateStore: RecoverableOperationStore<PendingCandidateCreate> =
  createWorkspaceRecoverableOperationStore<PendingCandidateCreate>(
    "multica_prompt_candidate_create",
  );

type CandidateCreateClient = Pick<
  ApiClient,
  "createPromptEvaluationOptimizationCandidate"
>;

async function execute(client: CandidateCreateClient, operation: PendingCandidateCreate) {
  return executeRecoverableMutation(
    () => client.createPromptEvaluationOptimizationCandidate(
      operation.runId,
      operation.requestKey,
    ),
    () => useCandidateCreateStore.getState().setPending(),
  );
}

export async function createPromptEvaluationOptimizationCandidateWithRecovery(
  runId: string,
  client: CandidateCreateClient = api,
): Promise<PromptEvaluationOptimizationCandidate> {
  const pending = useCandidateCreateStore.getState().pending;
  if (pending) {
    const recovered = await execute(client, pending);
    if (pending.runId === runId) return recovered;
  }
  const operation = { runId, requestKey: generateUUID(), createdAt: Date.now() };
  useCandidateCreateStore.getState().setPending(operation);
  return execute(client, operation);
}
