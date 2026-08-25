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

let volatileRequestFingerprint: string | null = null;

function fingerprintCredentialRequest(request: CreateExternalCredentialProfileRequest) {
  return JSON.stringify({
    provider: request.provider,
    name: request.name ?? "",
    capabilities: request.capabilities ?? {},
    verifyNow: Boolean(request.verify_now),
    tokenProvided: Boolean(request.token),
    secretRefProvided: Boolean(request.secret_ref),
  });
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
  const requestFingerprint = fingerprintCredentialRequest(request);
  const currentVolatileFingerprint = JSON.stringify(request);
  const pending = useCredentialProfileCreateStore.getState().pending;
  if (pending) {
    const recovered = await recoverPending(pending);
    const sameRequest = pending.requestFingerprint === requestFingerprint &&
      (volatileRequestFingerprint === null || volatileRequestFingerprint === currentVolatileFingerprint);
    volatileRequestFingerprint = null;
    if (recovered && sameRequest) return recovered;
  }
  const requestKey = generateUUID();
  useCredentialProfileCreateStore.getState().setPending({
    requestKey,
    requestFingerprint,
    createdAt: Date.now(),
  });
  volatileRequestFingerprint = currentVolatileFingerprint;
  const result = await executeRecoverableMutation(
    () => api.createExternalCredentialProfile(request, requestKey),
    () => useCredentialProfileCreateStore.getState().setPending(),
  );
  volatileRequestFingerprint = null;
  return result;
}
