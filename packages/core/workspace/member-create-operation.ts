"use client";

import { api } from "../api";
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

function fingerprintMemberRequest(request: CreateMemberRequest) {
  return JSON.stringify({
    account: request.account.trim().toLowerCase(),
    name: request.name?.trim() || request.account.trim().toLowerCase(),
    role: request.role ?? "member",
    passwordProvided: Boolean(request.password),
  });
}

async function recoverPending(
  workspaceId: string,
  pending: PendingMemberCreate,
) {
  const members = await api.listMembers(workspaceId);
  const recovered = members.find((member) => member.id === pending.requestKey) ?? null;
  useMemberCreateOperationStore.getState().setPending();
  return recovered;
}

export async function createMemberWithRecovery(
  workspaceId: string,
  request: CreateMemberRequest,
): Promise<MemberWithUser> {
  const requestFingerprint = fingerprintMemberRequest(request);
  const pending = useMemberCreateOperationStore.getState().pending;
  if (pending) {
    const recovered = await recoverPending(workspaceId, pending);
    if (recovered && pending.requestFingerprint === requestFingerprint) return recovered;
  }
  const requestKey = generateUUID();
  useMemberCreateOperationStore.getState().setPending({
    requestKey,
    requestFingerprint,
    createdAt: Date.now(),
  });
  return executeRecoverableMutation(
    () => api.createMember(workspaceId, request, requestKey),
    () => useMemberCreateOperationStore.getState().setPending(),
  );
}
