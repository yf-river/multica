"use client";

import { api, type ApiClient } from "../api";
import { executeRecoverableMutation } from "../api/transport";
import {
  createWorkspaceRecoverableOperationStore,
  type RecoverableOperationStore,
} from "../platform/recoverable-operation-store";
import type { CreateMemberRequest, MemberWithUser } from "../types";
import { generateUUID } from "../utils";

interface PendingMemberCreate {
  requestKey: string;
  requestFingerprint: string;
  createdAt: number;
}

const useMemberCreateOperationStore: RecoverableOperationStore<PendingMemberCreate> =
  createWorkspaceRecoverableOperationStore<PendingMemberCreate>(
    "multica_member_create_operation",
  );

type MemberCreateClient = Pick<ApiClient, "createMember" | "listMembers">;

async function fingerprintMemberRequest(request: CreateMemberRequest) {
  const normalized = JSON.stringify({
    account: request.account.trim().toLowerCase(),
    name: request.name?.trim() || request.account.trim().toLowerCase(),
    role: request.role ?? "member",
    passwordProvided: Boolean(request.password),
  });
  const digest = await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode(normalized));
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function recoverPending(
  client: MemberCreateClient,
  workspaceId: string,
  pending: PendingMemberCreate,
) {
  const members = await client.listMembers(workspaceId);
  const recovered = members.find((member) => member.id === pending.requestKey) ?? null;
  useMemberCreateOperationStore.getState().setPending();
  return recovered;
}

export async function createMemberWithRecovery(
  workspaceId: string,
  request: CreateMemberRequest,
  client: MemberCreateClient = api,
): Promise<MemberWithUser> {
  const requestFingerprint = await fingerprintMemberRequest(request);
  const pending = useMemberCreateOperationStore.getState().pending;
  if (pending) {
    const recovered = await recoverPending(client, workspaceId, pending);
    if (recovered && pending.requestFingerprint === requestFingerprint) return recovered;
  }
  const requestKey = generateUUID();
  useMemberCreateOperationStore.getState().setPending({
    requestKey,
    requestFingerprint,
    createdAt: Date.now(),
  });
  return executeRecoverableMutation(
    () => client.createMember(workspaceId, request, requestKey),
    () => useMemberCreateOperationStore.getState().setPending(),
  );
}
