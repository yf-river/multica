// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { Issue } from "../types";
import { createIssueWithRecovery } from "./create-operation";

const issue = (id: string, title: string) => ({ id, title, identifier: `ISS-${id}` }) as Issue;

describe("createIssueWithRecovery", () => {
  beforeEach(() => {
    setCurrentWorkspace("test-account", "workspace-1");
    localStorage.clear();
  });

  it("keeps the exact request and key after an unknown outcome", async () => {
    const createIssue = vi.fn()
      .mockRejectedValueOnce(
        new ApiTransportError("POST /api/issues", true, new Error("lost")),
      )
      .mockResolvedValueOnce(issue("1", "Child"))
      .mockResolvedValueOnce(issue("2", "Changed"));

    await expect(createIssueWithRecovery({ title: "Child" }, { createIssue }))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createIssueWithRecovery({ title: "Changed" }, { createIssue }))
      .resolves.toMatchObject({ id: "2" });

    expect(createIssue).toHaveBeenCalledTimes(3);
    expect(createIssue.mock.calls[0]?.[0]).toEqual({ title: "Child" });
    expect(createIssue.mock.calls[0]?.[1]).toMatch(/^[0-9a-f-]{36}$/);
    expect(createIssue.mock.calls[1]?.[1]).toBe(createIssue.mock.calls[0]?.[1]);
    expect(createIssue.mock.calls[2]?.[0]).toEqual({ title: "Changed" });
  });

  it("recovers an older pending request before creating a different one", async () => {
    const createIssue = vi.fn()
      .mockRejectedValueOnce(
        new ApiTransportError("POST /api/issues", true, new Error("lost")),
      )
      .mockResolvedValueOnce(issue("1", "Earlier"))
      .mockResolvedValueOnce(issue("2", "Current"));
    const client = { createIssue };

    await expect(createIssueWithRecovery({ title: "Earlier" }, client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createIssueWithRecovery({ title: "Current" }, client))
      .resolves.toMatchObject({ id: "2" });
    expect(createIssue).toHaveBeenCalledTimes(3);
    expect(createIssue.mock.calls[1]).toEqual([
      { title: "Earlier" },
      createIssue.mock.calls[0]?.[1],
    ]);
    expect(createIssue.mock.calls[2]?.[0]).toEqual({ title: "Current" });
  });
});
