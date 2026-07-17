// @vitest-environment jsdom

import { webcrypto } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiTransportError, getApi, setApiInstance } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { MemberWithUser } from "../types";
import { createMemberWithRecovery } from "./member-create-operation";

const member = (id: string) => ({ id }) as MemberWithUser;
let workspaceSequence = 0;

describe("createMemberWithRecovery", () => {
  beforeEach(async () => {
    vi.restoreAllMocks();
    setApiInstance(new ApiClient("http://core.test"));
    vi.stubGlobal("crypto", webcrypto);
    localStorage.clear();
    workspaceSequence += 1;
    setCurrentWorkspace(`member-create-${workspaceSequence}`, `workspace-${workspaceSequence}`);
    await Promise.resolve();
    await Promise.resolve();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("never persists the member password after an unknown outcome", async () => {
    vi.spyOn(getApi(), "createMember").mockRejectedValue(
      new ApiTransportError("POST member", true, new Error("lost")),
    );
    await expect(createMemberWithRecovery(`workspace-${workspaceSequence}`, {
      account: "new-member", password: "member-password-sentinel", role: "member",
    })).rejects.toBeInstanceOf(ApiTransportError);

    const stored = Array.from({ length: localStorage.length }, (_, index) => {
      const key = localStorage.key(index);
      return key ? localStorage.getItem(key) : "";
    }).join("");
    expect(stored).not.toContain("member-password-sentinel");
    expect(stored).toMatch(/[0-9a-f]{64}/);
  });

  it("recovers a committed member by its deterministic request id", async () => {
    const request = {
      account: "recovered-member", password: "RecoveredMember1!", role: "member" as const,
    };
    let requestKey = "";
    const createMember = vi.spyOn(getApi(), "createMember").mockImplementation((_: string, __: unknown, key?: string) => {
      requestKey = key ?? "";
      return Promise.reject(new ApiTransportError("POST member", true, new Error("lost")));
    });
    const listMembers = vi.spyOn(getApi(), "listMembers")
      .mockImplementation(() => Promise.resolve([member(requestKey)]));
    await expect(createMemberWithRecovery(`workspace-${workspaceSequence}`, request))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createMemberWithRecovery(`workspace-${workspaceSequence}`, request))
      .resolves.toMatchObject({ id: requestKey });
    expect(listMembers).toHaveBeenCalledWith(`workspace-${workspaceSequence}`);
    expect(createMember).toHaveBeenCalledTimes(1);
  });

  it("creates the current member when the previous request did not commit", async () => {
    vi.spyOn(getApi(), "createMember")
      .mockRejectedValueOnce(new ApiTransportError("POST missing member", true, new Error("lost")))
      .mockResolvedValueOnce(member("member-current"));
    vi.spyOn(getApi(), "listMembers").mockResolvedValue([]);
    await expect(createMemberWithRecovery(`workspace-${workspaceSequence}`, {
      account: "previous-member", password: "PreviousMember1!",
    })).rejects.toBeInstanceOf(ApiTransportError);
    await expect(createMemberWithRecovery(`workspace-${workspaceSequence}`, {
      account: "current-member", password: "CurrentMember1!",
    })).resolves.toMatchObject({ id: "member-current" });
  });
});
