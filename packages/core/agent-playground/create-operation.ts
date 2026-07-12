"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { api, isMutationOutcomeUnknown } from "../api";
import { defaultStorage } from "../platform/storage";
import { createWorkspaceAwareStorage, registerWorkspacePersistStore } from "../platform/workspace-storage";
import type { AgentPlaygroundDetail, CreateAgentPlaygroundExperimentRequest } from "../types";
import { generateUUID } from "../utils";

interface PendingAgentPlaygroundCreate {
  request: CreateAgentPlaygroundExperimentRequest;
  requestKey: string;
  createdAt: number;
}

interface AgentPlaygroundCreateState {
  pending?: PendingAgentPlaygroundCreate;
  setPending: (pending?: PendingAgentPlaygroundCreate) => void;
}

export const useAgentPlaygroundCreateStore = create<AgentPlaygroundCreateState>()(
  persist(
    (set) => ({ setPending: (pending) => set({ pending }) }),
    {
      name: "multica_agent_playground_create",
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

registerWorkspacePersistStore(useAgentPlaygroundCreateStore);

export interface AgentPlaygroundCreateClient {
  createAgentPlaygroundExperiment(
    request: CreateAgentPlaygroundExperimentRequest,
    requestKey: string,
  ): Promise<AgentPlaygroundDetail>;
}

async function execute(
  client: AgentPlaygroundCreateClient,
  operation: PendingAgentPlaygroundCreate,
) {
  try {
    const detail = await client.createAgentPlaygroundExperiment(operation.request, operation.requestKey);
    useAgentPlaygroundCreateStore.getState().setPending();
    return detail;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) {
      useAgentPlaygroundCreateStore.getState().setPending();
    }
    throw error;
  }
}

export async function createAgentPlaygroundExperimentWithRecovery(
  request: CreateAgentPlaygroundExperimentRequest,
  client: AgentPlaygroundCreateClient = api,
): Promise<AgentPlaygroundDetail> {
  const pending = useAgentPlaygroundCreateStore.getState().pending;
  if (pending) {
    const recovered = await execute(client, pending);
    if (JSON.stringify(pending.request) === JSON.stringify(request)) return recovered;
  }
  const operation = { request, requestKey: generateUUID(), createdAt: Date.now() };
  useAgentPlaygroundCreateStore.getState().setPending(operation);
  return execute(client, operation);
}
