import { api, type ApiClient } from "../api";
import { executeRecoverableIntent, sameMutationRequest } from "../api/transport";
import {
  createWorkspaceAwareStorage,
  registerWorkspacePersistStore,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";
import type {
  CreatePromptLibraryItemRequest,
  CreatePromptLibraryTrialRequest,
  CreatePromptLibraryVersionRequest,
  CreatePromptLibraryVersionResponse,
  PromptLibraryItem,
  PromptLibraryTrial,
} from "../types";
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

interface PendingItemCreate {
  request: CreatePromptLibraryItemRequest;
  requestKey: string;
  createdAt: number;
}

interface PendingVersionCreate {
  promptId: string;
  request: CreatePromptLibraryVersionRequest;
  requestKey: string;
  createdAt: number;
}

interface PromptLibraryCreateState {
  pending: Record<string, PendingTrialCreate>;
  item?: PendingItemCreate;
  versions: Record<string, PendingVersionCreate>;
  setPending: (scope: string, operation?: PendingTrialCreate) => void;
  setItem: (operation?: PendingItemCreate) => void;
  setVersion: (promptId: string, operation?: PendingVersionCreate) => void;
}

const usePromptLibraryCreateStore = create<PromptLibraryCreateState>()(
  persist((set) => ({
    pending: {},
    versions: {},
    setPending: (scope, operation) => set((state) => {
      const pending = { ...state.pending };
      if (operation) pending[scope] = operation;
      else delete pending[scope];
      return { pending };
    }),
    setItem: (item) => set({ item }),
    setVersion: (promptId, operation) => set((state) => {
      const versions = { ...state.versions };
      if (operation) versions[promptId] = operation;
      else delete versions[promptId];
      return { versions };
    }),
  }), {
    name: "multica_prompt_library_trial_create",
    storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    partialize: ({ pending, item, versions }) => ({ pending, item, versions }),
    onRehydrateStorage: () => (state) => {
      if (!state) return;
      const cutoff = Date.now() - 30 * 24 * 60 * 60 * 1000;
      state.pending = Object.fromEntries(
        Object.entries(state.pending).filter(([, operation]) => operation.createdAt >= cutoff),
      );
      if (state.item && state.item.createdAt < cutoff) state.item = undefined;
      state.versions = Object.fromEntries(
        Object.entries(state.versions ?? {}).filter(([, operation]) => operation.createdAt >= cutoff),
      );
    },
  },
));

registerWorkspacePersistStore(usePromptLibraryCreateStore);

type PromptLibraryTrialCreateClient = Pick<ApiClient, "createPromptLibraryTrial">;

const scopeFor = (promptId: string, versionId: string, request: CreatePromptLibraryTrialRequest) =>
  `${promptId}:${versionId}:${request.agent_id}`;

export async function createPromptLibraryTrialWithRecovery(
  promptId: string,
  versionId: string,
  request: CreatePromptLibraryTrialRequest,
  client: PromptLibraryTrialCreateClient = api,
): Promise<PromptLibraryTrial> {
  const scope = scopeFor(promptId, versionId, request);
  const pending = usePromptLibraryCreateStore.getState().pending[scope];
  return executeRecoverableIntent(
    pending,
    (operation) => sameMutationRequest(operation.request, request),
    () => ({
      promptId,
      versionId,
      request,
      requestKey: generateUUID(),
      createdAt: Date.now(),
    }),
    (operation) => usePromptLibraryCreateStore.getState().setPending(scope, operation),
    (operation) => client.createPromptLibraryTrial(
      operation.promptId,
      operation.versionId,
      operation.request,
      operation.requestKey,
    ),
  );
}

export async function createPromptLibraryItemWithRecovery(
  request: CreatePromptLibraryItemRequest,
  client: Pick<ApiClient, "createPromptLibraryItem"> = api,
): Promise<PromptLibraryItem> {
  const pending = usePromptLibraryCreateStore.getState().item;
  return executeRecoverableIntent(
    pending,
    (operation) => sameMutationRequest(operation.request, request),
    () => ({ request, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => usePromptLibraryCreateStore.getState().setItem(operation),
    (operation) => client.createPromptLibraryItem(operation.request, operation.requestKey),
  );
}

export async function createPromptLibraryVersionWithRecovery(
  promptId: string,
  request: CreatePromptLibraryVersionRequest,
  client: Pick<ApiClient, "createPromptLibraryVersion"> = api,
): Promise<CreatePromptLibraryVersionResponse> {
  const pending = usePromptLibraryCreateStore.getState().versions[promptId];
  return executeRecoverableIntent(
    pending,
    (operation) => sameMutationRequest(operation.request, request),
    () => ({ promptId, request, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => usePromptLibraryCreateStore.getState().setVersion(promptId, operation),
    (operation) => client.createPromptLibraryVersion(
      operation.promptId,
      operation.request,
      operation.requestKey,
    ),
  );
}
