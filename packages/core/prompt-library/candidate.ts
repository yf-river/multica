"use client";

import { api } from "../api";
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
): Promise<{ id: string }> {
  return executeRecoverableIntent(
    candidateCreateStore.getState().pending,
    (operation) => operation.runId === runId,
    () => ({ runId, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => candidateCreateStore.getState().setPending(operation),
    (operation) => api.createPromptEvaluationOptimizationCandidate(
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

function decide(
  kind: CandidateDecision["kind"],
  candidateId: string,
  reason: string,
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
      ? api.publishPromptEvaluationOptimizationCandidate(operation.candidateId, operation.requestKey)
      : api.rejectPromptEvaluationOptimizationCandidate(
          operation.candidateId, { reason: operation.reason || undefined }, operation.requestKey,
        ),
  );
}

export function publishPromptEvaluationOptimizationCandidateWithRecovery(
  candidateId: string,
): Promise<string> {
  return decide("publish", candidateId, "");
}

export function rejectPromptEvaluationOptimizationCandidateWithRecovery(
  candidateId: string,
  reason: string,
): Promise<PromptEvaluationOptimizationCandidateStatus> {
  return decide("reject", candidateId, reason) as Promise<PromptEvaluationOptimizationCandidateStatus>;
}
