"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { api, isMutationOutcomeUnknown } from "../api";
import { createWorkspaceAwareStorage, registerWorkspacePersistStore } from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";
import type { CreateMemberRequest, MemberWithUser } from "../types";
import { generateUUID } from "../utils";

interface PendingMemberCreate {
  requestKey: string;
  requestFingerprint: string;
  createdAt: number;
}

interface MemberCreateOperationState {
  pending?: PendingMemberCreate;
  setPending: (pending?: PendingMemberCreate) => void;
}

export const useMemberCreateOperationStore = create<MemberCreateOperationState>()(
  persist(
    (set) => ({ setPending: (pending) => set({ pending }) }),
    {
      name: "multica_member_create_operation",
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

registerWorkspacePersistStore(useMemberCreateOperationStore);

export interface MemberCreateClient {
  createMember(workspaceId: string, request: CreateMemberRequest, requestKey: string): Promise<MemberWithUser>;
  listMembers(workspaceId: string): Promise<MemberWithUser[]>;
}

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

async function execute(
  client: MemberCreateClient,
  workspaceId: string,
  request: CreateMemberRequest,
  requestKey: string,
) {
  try {
    const member = await client.createMember(workspaceId, request, requestKey);
    useMemberCreateOperationStore.getState().setPending();
    return member;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) {
      useMemberCreateOperationStore.getState().setPending();
    }
    throw error;
  }
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
  return execute(client, workspaceId, request, requestKey);
}
