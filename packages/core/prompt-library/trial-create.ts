import { api, isMutationOutcomeUnknown } from "../api";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";
import type { CreatePromptLibraryTrialRequest, PromptLibraryTrial } from "../types";
import { generateUUID } from "../utils";
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";

interface PendingTrialCreate {
  promptId: string;
  versionId: string;
  request: CreatePromptLibraryTrialRequest;
  requestKey: string;
  createdAt: number;
}

interface TrialCreateState {
  pending: Record<string, PendingTrialCreate>;
  setPending: (scope: string, operation?: PendingTrialCreate) => void;
}

export const usePromptLibraryTrialCreateStore = create<TrialCreateState>()(
  persist((set) => ({
    pending: {},
    setPending: (scope, operation) => set((state) => {
      const pending = { ...state.pending };
      if (operation) pending[scope] = operation;
      else delete pending[scope];
      return { pending };
    }),
  }), {
    name: "multica_prompt_library_trial_create",
    storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    partialize: ({ pending }) => ({ pending }),
    onRehydrateStorage: () => (state) => {
      if (!state) return;
      const cutoff = Date.now() - 30 * 24 * 60 * 60 * 1000;
      state.pending = Object.fromEntries(
        Object.entries(state.pending).filter(([, operation]) => operation.createdAt >= cutoff),
      );
    },
  },
));

registerWorkspacePersistStore(usePromptLibraryTrialCreateStore);

export interface PromptLibraryTrialCreateClient {
  createPromptLibraryTrial(
    promptId: string,
    versionId: string,
    request: CreatePromptLibraryTrialRequest,
    requestKey: string,
  ): Promise<PromptLibraryTrial>;
}

const scopeFor = (promptId: string, versionId: string, request: CreatePromptLibraryTrialRequest) =>
  `${promptId}:${versionId}:${request.agent_id}`;

async function execute(
  client: PromptLibraryTrialCreateClient,
  scope: string,
  operation: PendingTrialCreate,
) {
  try {
    const trial = await client.createPromptLibraryTrial(
      operation.promptId,
      operation.versionId,
      operation.request,
      operation.requestKey,
    );
    usePromptLibraryTrialCreateStore.getState().setPending(scope);
    return trial;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) {
      usePromptLibraryTrialCreateStore.getState().setPending(scope);
    }
    throw error;
  }
}

export async function createPromptLibraryTrialWithRecovery(
  promptId: string,
  versionId: string,
  request: CreatePromptLibraryTrialRequest,
  client: PromptLibraryTrialCreateClient = api,
): Promise<PromptLibraryTrial> {
  const scope = scopeFor(promptId, versionId, request);
  const pending = usePromptLibraryTrialCreateStore.getState().pending[scope];
  if (pending) {
    const recovered = await execute(client, scope, pending);
    if (JSON.stringify(pending.request) === JSON.stringify(request)) return recovered;
  }
  const operation = {
    promptId,
    versionId,
    request,
    requestKey: generateUUID(),
    createdAt: Date.now(),
  };
  usePromptLibraryTrialCreateStore.getState().setPending(scope, operation);
  return execute(client, scope, operation);
}
