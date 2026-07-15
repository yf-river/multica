"use client";

import { api, type ApiClient } from "../api";
import { executeRecoverableIntent } from "../api/transport";
import {
  createWorkspaceRecoverableOperationStore,
  type RecoverableOperationStore,
} from "../platform/recoverable-operation-store";
import type { PromptEvaluationOptimizationCandidateStatus } from "../types";
import { generateUUID } from "../utils";

interface PendingCandidateCreate {
  runId: string;
  requestKey: string;
  createdAt: number;
}

const candidateCreateStore: RecoverableOperationStore<PendingCandidateCreate> =
  createWorkspaceRecoverableOperationStore<PendingCandidateCreate>(
    "multica_prompt_candidate_create",
  );

export function createPromptEvaluationOptimizationCandidateWithRecovery(
  runId: string,
  client: Pick<ApiClient, "createPromptEvaluationOptimizationCandidate"> = api,
): Promise<{ id: string }> {
  return executeRecoverableIntent(
    candidateCreateStore.getState().pending,
    (operation) => operation.runId === runId,
    () => ({ runId, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => candidateCreateStore.getState().setPending(operation),
    (operation) => client.createPromptEvaluationOptimizationCandidate(
      operation.runId,
      operation.requestKey,
    ),
  );
}

type CandidateDecision =
  | { kind: "publish"; candidateId: string; requestKey: string; createdAt: number }
  | { kind: "reject"; candidateId: string; reason: string; requestKey: string; createdAt: number };

const candidateDecisionStore: RecoverableOperationStore<CandidateDecision> =
  createWorkspaceRecoverableOperationStore<CandidateDecision>(
    "multica_prompt_candidate_decision",
  );

type CandidateDecisionClient = Pick<
  ApiClient,
  | "publishPromptEvaluationOptimizationCandidate"
  | "rejectPromptEvaluationOptimizationCandidate"
>;

function decide(
  kind: CandidateDecision["kind"],
  candidateId: string,
  reason: string,
  client: CandidateDecisionClient,
) {
  return executeRecoverableIntent(
    candidateDecisionStore.getState().pending,
    (pending) => pending.kind === kind
      && pending.candidateId === candidateId
      && (pending.kind === "publish" || pending.reason === reason),
    () => kind === "publish"
      ? { kind, candidateId, requestKey: generateUUID(), createdAt: Date.now() }
      : { kind, candidateId, reason, requestKey: generateUUID(), createdAt: Date.now() },
    (operation) => candidateDecisionStore.getState().setPending(operation),
    (operation) => operation.kind === "publish"
      ? client.publishPromptEvaluationOptimizationCandidate(operation.candidateId, operation.requestKey)
      : client.rejectPromptEvaluationOptimizationCandidate(
          operation.candidateId, { reason: operation.reason || undefined }, operation.requestKey,
        ),
  );
}

export function publishPromptEvaluationOptimizationCandidateWithRecovery(
  candidateId: string,
  client: CandidateDecisionClient = api,
): Promise<string> {
  return decide("publish", candidateId, "", client);
}

export function rejectPromptEvaluationOptimizationCandidateWithRecovery(
  candidateId: string,
  reason: string,
  client: CandidateDecisionClient = api,
): Promise<PromptEvaluationOptimizationCandidateStatus> {
  return decide("reject", candidateId, reason, client) as Promise<PromptEvaluationOptimizationCandidateStatus>;
}
