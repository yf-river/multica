"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { api, isMutationOutcomeUnknown } from "../api";
import { defaultStorage } from "../platform/storage";
import { createWorkspaceAwareStorage, registerWorkspacePersistStore } from "../platform/workspace-storage";
import type { PromptEvaluationSkillReEvalRunResponse, RunPromptEvaluationSkillReEvalRequest } from "../types";
import { generateUUID } from "../utils";

interface PendingSkillReEvalRun {
  candidateId: string;
  request: RunPromptEvaluationSkillReEvalRequest;
  requestKey: string;
  createdAt: number;
}

interface SkillReEvalRunState {
  pending?: PendingSkillReEvalRun;
  setPending: (pending?: PendingSkillReEvalRun) => void;
}

export const useSkillReEvalRunStore = create<SkillReEvalRunState>()(
  persist(
    (set) => ({ setPending: (pending) => set({ pending }) }),
    {
      name: "multica_skill_re_eval_run",
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

registerWorkspacePersistStore(useSkillReEvalRunStore);

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
