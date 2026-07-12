"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { api, isMutationOutcomeUnknown } from "../api";
import { defaultStorage } from "../platform/storage";
import { createWorkspaceAwareStorage, registerWorkspacePersistStore } from "../platform/workspace-storage";
import type { PromptEvaluationOptimizationCandidate } from "../types";
import { generateUUID } from "../utils";

interface PendingCandidateCreate {
  runId: string;
  requestKey: string;
  createdAt: number;
}

interface CandidateCreateState {
  pending?: PendingCandidateCreate;
  setPending: (pending?: PendingCandidateCreate) => void;
}

export const useCandidateCreateStore = create<CandidateCreateState>()(
  persist(
    (set) => ({ setPending: (pending) => set({ pending }) }),
    {
      name: "multica_prompt_candidate_create",
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

registerWorkspacePersistStore(useCandidateCreateStore);

export interface CandidateCreateClient {
  createPromptEvaluationOptimizationCandidate(
    runId: string,
    requestKey: string,
  ): Promise<PromptEvaluationOptimizationCandidate>;
}

async function execute(client: CandidateCreateClient, operation: PendingCandidateCreate) {
  try {
    const response = await client.createPromptEvaluationOptimizationCandidate(
      operation.runId,
      operation.requestKey,
    );
    useCandidateCreateStore.getState().setPending();
    return response;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) useCandidateCreateStore.getState().setPending();
    throw error;
  }
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
