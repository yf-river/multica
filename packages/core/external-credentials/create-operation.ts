"use client";

import { api, ApiError } from "../api";
import { executeRecoverableMutation } from "../api/transport";
import {
  createAccountRecoverableOperationStore,
  type RecoverableOperationStore,
} from "../platform/recoverable-operation-store";
import type { CreateExternalCredentialProfileRequest, ExternalCredentialProfile } from "../types";
import { generateUUID } from "../utils";

interface PendingCredentialProfileCreate {
  requestKey: string;
  requestFingerprint: string;
  createdAt: number;
}

const useCredentialProfileCreateStore: RecoverableOperationStore<PendingCredentialProfileCreate> =
  createAccountRecoverableOperationStore<PendingCredentialProfileCreate>(
    "multica_credential_profile_create",
  );

async function fingerprintCredentialRequest(request: CreateExternalCredentialProfileRequest) {
  const bytes = new TextEncoder().encode(JSON.stringify(request));
  const digest = await globalThis.crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function recoverPending(
  pending: PendingCredentialProfileCreate,
) {
  try {
    const profile = await api.getExternalCredentialProfile(pending.requestKey);
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

export async function createExternalCredentialProfileWithRecovery(
  request: CreateExternalCredentialProfileRequest,
): Promise<ExternalCredentialProfile> {
  const requestFingerprint = await fingerprintCredentialRequest(request);
  const pending = useCredentialProfileCreateStore.getState().pending;
  if (pending) {
    const recovered = await recoverPending(pending);
    if (recovered && pending.requestFingerprint === requestFingerprint) return recovered;
  }
  const requestKey = generateUUID();
  useCredentialProfileCreateStore.getState().setPending({
    requestKey,
    requestFingerprint,
    createdAt: Date.now(),
  });
  return executeRecoverableMutation(
    () => api.createExternalCredentialProfile(request, requestKey),
    () => useCredentialProfileCreateStore.getState().setPending(),
  );
}
