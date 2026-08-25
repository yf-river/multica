// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiTransportError, getApi, setApiInstance } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { Issue } from "../types";
import { createIssueWithRecovery } from "./create-operation";

const issue = (id: string, title: string) => ({ id, title, identifier: `ISS-${id}` }) as Issue;

describe("createIssueWithRecovery", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setApiInstance(new ApiClient("http://core.test"));
    setCurrentWorkspace("test-account", "workspace-1");
    localStorage.clear();
  });

  it("keeps the exact request and key after an unknown outcome", async () => {
    const createIssue = vi.spyOn(getApi(), "createIssue")
      .mockRejectedValueOnce(
        new ApiTransportError("POST /api/issues", true, new Error("lost")),
      )
      .mockResolvedValueOnce(issue("1", "Child"))
      .mockResolvedValueOnce(issue("2", "Changed"));

    await expect(createIssueWithRecovery({ title: "Child" }))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createIssueWithRecovery({ title: "Changed" }))
      .resolves.toMatchObject({ id: "2" });

    expect(createIssue).toHaveBeenCalledTimes(3);
    expect(createIssue.mock.calls[0]?.[0]).toEqual({ title: "Child" });
    expect(createIssue.mock.calls[0]?.[1]).toMatch(/^[0-9a-f-]{36}$/);
    expect(createIssue.mock.calls[1]?.[1]).toBe(createIssue.mock.calls[0]?.[1]);
    expect(createIssue.mock.calls[2]?.[0]).toEqual({ title: "Changed" });
  });

  it("recovers an older pending request before creating a different one", async () => {
    const createIssue = vi.spyOn(getApi(), "createIssue")
      .mockRejectedValueOnce(
        new ApiTransportError("POST /api/issues", true, new Error("lost")),
      )
      .mockResolvedValueOnce(issue("1", "Earlier"))
      .mockResolvedValueOnce(issue("2", "Current"));
    await expect(createIssueWithRecovery({ title: "Earlier" }))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createIssueWithRecovery({ title: "Current" }))
      .resolves.toMatchObject({ id: "2" });
    expect(createIssue).toHaveBeenCalledTimes(3);
    expect(createIssue.mock.calls[1]).toEqual([
      { title: "Earlier" },
      createIssue.mock.calls[0]?.[1],
    ]);
    expect(createIssue.mock.calls[2]?.[0]).toEqual({ title: "Current" });
  });
});
