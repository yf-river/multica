"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { api, isMutationOutcomeUnknown } from "../api";
import { defaultStorage } from "../platform/storage";
import { createWorkspaceAwareStorage, registerWorkspacePersistStore } from "../platform/workspace-storage";
import type {
  PreparePromptEvaluationSkillReEvalRequest,
  PromptEvaluationSkillReEvalAssetResponse,
} from "../types";
import { generateUUID } from "../utils";

interface PendingSkillReEvalAsset {
  candidateId: string;
  request: PreparePromptEvaluationSkillReEvalRequest;
  requestKey: string;
  createdAt: number;
}

interface SkillReEvalAssetState {
  pending?: PendingSkillReEvalAsset;
  setPending: (pending?: PendingSkillReEvalAsset) => void;
}

export const useSkillReEvalAssetStore = create<SkillReEvalAssetState>()(
  persist(
    (set) => ({ setPending: (pending) => set({ pending }) }),
    {
      name: "multica_skill_re_eval_asset",
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

registerWorkspacePersistStore(useSkillReEvalAssetStore);

export interface SkillReEvalAssetClient {
  preparePromptEvaluationSkillReEvalAsset(
    candidateId: string,
    request: PreparePromptEvaluationSkillReEvalRequest,
    requestKey: string,
  ): Promise<PromptEvaluationSkillReEvalAssetResponse>;
}

async function execute(client: SkillReEvalAssetClient, operation: PendingSkillReEvalAsset) {
  try {
    const response = await client.preparePromptEvaluationSkillReEvalAsset(
      operation.candidateId,
      operation.request,
      operation.requestKey,
    );
    useSkillReEvalAssetStore.getState().setPending();
    return response;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) useSkillReEvalAssetStore.getState().setPending();
    throw error;
  }
}

export async function preparePromptEvaluationSkillReEvalAssetWithRecovery(
  candidateId: string,
  request: PreparePromptEvaluationSkillReEvalRequest,
  client: SkillReEvalAssetClient = api,
): Promise<PromptEvaluationSkillReEvalAssetResponse> {
  const pending = useSkillReEvalAssetStore.getState().pending;
  if (pending) {
    const recovered = await execute(client, pending);
    if (pending.candidateId === candidateId && JSON.stringify(pending.request) === JSON.stringify(request)) {
      return recovered;
    }
  }
  const operation = { candidateId, request, requestKey: generateUUID(), createdAt: Date.now() };
  useSkillReEvalAssetStore.getState().setPending(operation);
  return execute(client, operation);
}
