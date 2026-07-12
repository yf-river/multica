"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { api, isMutationOutcomeUnknown } from "../api";
import { defaultStorage } from "../platform/storage";
import { createWorkspaceAwareStorage, registerWorkspacePersistStore } from "../platform/workspace-storage";
import type {
  PromptEvaluationOptimizationCandidate,
  PublishPromptEvaluationOptimizationCandidateResponse,
} from "../types";
import { generateUUID } from "../utils";

type CandidateDecision =
  | { kind: "publish"; candidateId: string; requestKey: string; createdAt: number }
  | { kind: "reject"; candidateId: string; reason: string; requestKey: string; createdAt: number };

interface CandidateDecisionState {
  pending?: CandidateDecision;
  setPending: (pending?: CandidateDecision) => void;
}

export const useCandidateDecisionStore = create<CandidateDecisionState>()(
  persist(
    (set) => ({ setPending: (pending) => set({ pending }) }),
    {
      name: "multica_prompt_candidate_decision",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      partialize: ({ pending }) => ({ pending }),
      onRehydrateStorage: () => (state) => {
        if (state?.pending && state.pending.createdAt < Date.now() - 30 * 24 * 60 * 60 * 1000) {
          state.pending = undefined;
        }
      },
    },
  ),
);

registerWorkspacePersistStore(useCandidateDecisionStore);

export interface CandidateDecisionClient {
  publishPromptEvaluationOptimizationCandidate(
    candidateId: string,
    requestKey: string,
  ): Promise<PublishPromptEvaluationOptimizationCandidateResponse>;
  rejectPromptEvaluationOptimizationCandidate(
    candidateId: string,
    request: { reason?: string },
    requestKey: string,
  ): Promise<PromptEvaluationOptimizationCandidate>;
}

async function execute(client: CandidateDecisionClient, decision: CandidateDecision) {
  try {
    const response = decision.kind === "publish"
      ? await client.publishPromptEvaluationOptimizationCandidate(decision.candidateId, decision.requestKey)
      : await client.rejectPromptEvaluationOptimizationCandidate(
          decision.candidateId, { reason: decision.reason || undefined }, decision.requestKey,
        );
    useCandidateDecisionStore.getState().setPending();
    return response;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) useCandidateDecisionStore.getState().setPending();
    throw error;
  }
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
