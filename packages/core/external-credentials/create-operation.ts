"use client";

import { api, ApiError, type ApiClient } from "../api";
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

type CredentialProfileCreateClient = Pick<
  ApiClient,
  "createExternalCredentialProfile" | "getExternalCredentialProfile"
>;

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
  return executeRecoverableMutation(
    () => client.createExternalCredentialProfile(request, requestKey),
    () => useCredentialProfileCreateStore.getState().setPending(),
  );
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
