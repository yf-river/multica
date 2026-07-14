"use client";

import { api, type ApiClient } from "../api";
import { executeRecoverableMutation } from "../api/transport";
import {
  createWorkspaceRecoverableOperationStore,
  type RecoverableOperationStore,
} from "../platform/recoverable-operation-store";
import type {
  PromptEvaluationOptimizationCandidate,
  PublishPromptEvaluationOptimizationCandidateResponse,
} from "../types";
import { generateUUID } from "../utils";

type CandidateDecision =
  | { kind: "publish"; candidateId: string; requestKey: string; createdAt: number }
  | { kind: "reject"; candidateId: string; reason: string; requestKey: string; createdAt: number };

const useCandidateDecisionStore: RecoverableOperationStore<CandidateDecision> =
  createWorkspaceRecoverableOperationStore<CandidateDecision>(
    "multica_prompt_candidate_decision",
  );

type CandidateDecisionClient = Pick<
  ApiClient,
  | "publishPromptEvaluationOptimizationCandidate"
  | "rejectPromptEvaluationOptimizationCandidate"
>;

async function execute(
  client: CandidateDecisionClient,
  decision: CandidateDecision,
): Promise<
  | PromptEvaluationOptimizationCandidate
  | PublishPromptEvaluationOptimizationCandidateResponse
> {
  return executeRecoverableMutation<
    | PromptEvaluationOptimizationCandidate
    | PublishPromptEvaluationOptimizationCandidateResponse
  >(
    () => decision.kind === "publish"
      ? client.publishPromptEvaluationOptimizationCandidate(decision.candidateId, decision.requestKey)
      : client.rejectPromptEvaluationOptimizationCandidate(
          decision.candidateId, { reason: decision.reason || undefined }, decision.requestKey,
        ),
    () => useCandidateDecisionStore.getState().setPending(),
  );
}

async function recoverPending(client: CandidateDecisionClient) {
  const pending = useCandidateDecisionStore.getState().pending;
  if (!pending) return undefined;
  return { pending, response: await execute(client, pending) };
}

export async function publishPromptEvaluationOptimizationCandidateWithRecovery(
  candidateId: string,
  client: CandidateDecisionClient = api,
): Promise<PublishPromptEvaluationOptimizationCandidateResponse> {
  const recovered = await recoverPending(client);
  if (recovered?.pending.kind === "publish" && recovered.pending.candidateId === candidateId) {
    return recovered.response as PublishPromptEvaluationOptimizationCandidateResponse;
  }
  const decision: CandidateDecision = {
    kind: "publish", candidateId, requestKey: generateUUID(), createdAt: Date.now(),
  };
  useCandidateDecisionStore.getState().setPending(decision);
  return execute(client, decision) as Promise<PublishPromptEvaluationOptimizationCandidateResponse>;
}

export async function rejectPromptEvaluationOptimizationCandidateWithRecovery(
  candidateId: string,
  reason: string,
  client: CandidateDecisionClient = api,
): Promise<PromptEvaluationOptimizationCandidate> {
  const recovered = await recoverPending(client);
  if (recovered?.pending.kind === "reject"
    && recovered.pending.candidateId === candidateId
    && recovered.pending.reason === reason) {
    return recovered.response as PromptEvaluationOptimizationCandidate;
  }
  const decision: CandidateDecision = {
    kind: "reject", candidateId, reason, requestKey: generateUUID(), createdAt: Date.now(),
  };
  useCandidateDecisionStore.getState().setPending(decision);
  return execute(client, decision) as Promise<PromptEvaluationOptimizationCandidate>;
}
