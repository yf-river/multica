// @vitest-environment jsdom

import { webcrypto } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { MemberWithUser } from "../types";
import {
  createMemberWithRecovery,
  useMemberCreateOperationStore,
} from "./member-create-operation";

const member = (id: string) => ({ id }) as MemberWithUser;

describe("createMemberWithRecovery", () => {
  beforeEach(async () => {
    vi.stubGlobal("crypto", webcrypto);
    localStorage.clear();
    setCurrentWorkspace("workspace-one", "workspace-1");
    await Promise.resolve();
    useMemberCreateOperationStore.setState({ pending: undefined });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("never persists the member password after an unknown outcome", async () => {
    const client = {
      createMember: vi.fn().mockRejectedValue(
        new ApiTransportError("POST member", true, new Error("lost")),
      ),
      listMembers: vi.fn(),
    };
    await expect(createMemberWithRecovery("workspace-1", {
      account: "new-member", password: "member-password-sentinel", role: "member",
    }, client)).rejects.toBeInstanceOf(ApiTransportError);

    const stored = Array.from({ length: localStorage.length }, (_, index) => {
      const key = localStorage.key(index);
      return key ? localStorage.getItem(key) : "";
    }).join("");
    expect(stored).not.toContain("member-password-sentinel");
    expect(useMemberCreateOperationStore.getState().pending?.requestFingerprint)
      .toMatch(/^[0-9a-f]{64}$/);
  });

  it("recovers a committed member by its deterministic request id", async () => {
	const request = {
	  account: "recovered-member", password: "RecoveredMember1!", role: "member" as const,
	};
	let requestKey = "";
	const createMember = vi.fn().mockImplementation((_: string, __: unknown, key: string) => {
	  requestKey = key;
	  return Promise.reject(new ApiTransportError("POST member", true, new Error("lost")));
	});
    const client = {
	  createMember,
	  listMembers: vi.fn().mockImplementation(() => Promise.resolve([member(requestKey)])),
    };
	await expect(createMemberWithRecovery("workspace-1", request, client))
	  .rejects.toBeInstanceOf(ApiTransportError);
	await expect(createMemberWithRecovery("workspace-1", request, client))
	  .resolves.toMatchObject({ id: requestKey });
    expect(client.listMembers).toHaveBeenCalledWith("workspace-1");
	expect(createMember).toHaveBeenCalledTimes(1);
  });

  it("creates the current member when the previous request did not commit", async () => {
    useMemberCreateOperationStore.getState().setPending({
      requestKey: "10000000-0000-4000-8000-000000000010",
      requestFingerprint: "b".repeat(64),
      createdAt: Date.now(),
    });
    const client = {
      createMember: vi.fn().mockResolvedValue(member("member-current")),
      listMembers: vi.fn().mockResolvedValue([]),
    };
    await expect(createMemberWithRecovery("workspace-1", {
      account: "current-member", password: "CurrentMember1!",
    }, client)).resolves.toMatchObject({ id: "member-current" });
  });
});
