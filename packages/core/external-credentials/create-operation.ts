"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { api, ApiError, isMutationOutcomeUnknown } from "../api";
import { defaultStorage } from "../platform/storage";
import { registerAccountPersistStore } from "../platform/workspace-storage";
import type { CreateExternalCredentialProfileRequest, ExternalCredentialProfile } from "../types";
import { generateUUID } from "../utils";

interface PendingCredentialProfileCreate {
  requestKey: string;
  requestFingerprint: string;
  createdAt: number;
}

interface CredentialProfileCreateState {
  pending?: PendingCredentialProfileCreate;
  setPending: (pending?: PendingCredentialProfileCreate) => void;
}

export const useCredentialProfileCreateStore = create<CredentialProfileCreateState>()(
  persist(
    (set) => ({ setPending: (pending) => set({ pending }) }),
    {
      name: "multica_credential_profile_create",
      storage: createJSONStorage(() => defaultStorage),
      partialize: ({ pending }) => ({ pending }),
      onRehydrateStorage: () => (state) => {
        if (state?.pending && state.pending.createdAt < Date.now() - 30 * 24 * 60 * 60 * 1000) {
          state.pending = undefined;
        }
      },
    },
  ),
);

registerAccountPersistStore(useCredentialProfileCreateStore);

export interface CredentialProfileCreateClient {
  createExternalCredentialProfile(
    request: CreateExternalCredentialProfileRequest,
    requestKey: string,
  ): Promise<ExternalCredentialProfile>;
  getExternalCredentialProfile(id: string): Promise<ExternalCredentialProfile>;
}

async function fingerprintCredentialRequest(request: CreateExternalCredentialProfileRequest) {
  const bytes = new TextEncoder().encode(JSON.stringify(request));
  const digest = await globalThis.crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function recoverPending(
  client: CredentialProfileCreateClient,
  pending: PendingCredentialProfileCreate,
) {
  try {
    const profile = await client.getExternalCredentialProfile(pending.requestKey);
    useCredentialProfileCreateStore.getState().setPending();
    return profile;
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) {
      useCredentialProfileCreateStore.getState().setPending();
      return null;
    }
    throw error;
  }
}

async function execute(
  client: CredentialProfileCreateClient,
  request: CreateExternalCredentialProfileRequest,
  requestKey: string,
) {
  try {
    const profile = await client.createExternalCredentialProfile(request, requestKey);
    useCredentialProfileCreateStore.getState().setPending();
    return profile;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) {
      useCredentialProfileCreateStore.getState().setPending();
    }
    throw error;
  }
}

export async function createExternalCredentialProfileWithRecovery(
  request: CreateExternalCredentialProfileRequest,
  client: CredentialProfileCreateClient = api,
): Promise<ExternalCredentialProfile> {
  const requestFingerprint = await fingerprintCredentialRequest(request);
  const pending = useCredentialProfileCreateStore.getState().pending;
  if (pending) {
    const recovered = await recoverPending(client, pending);
    if (recovered && pending.requestFingerprint === requestFingerprint) return recovered;
  }
  const requestKey = generateUUID();
  useCredentialProfileCreateStore.getState().setPending({
    requestKey,
    requestFingerprint,
    createdAt: Date.now(),
  });
  return execute(client, request, requestKey);
}
