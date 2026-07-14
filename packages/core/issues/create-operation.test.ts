// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { Issue } from "../types";
import { createIssueWithRecovery } from "./create-operation";
import { useIssueCreatePendingStore } from "./issue-create-pending-store";

const issue = (id: string, title: string) => ({ id, title, identifier: `ISS-${id}` }) as Issue;

describe("createIssueWithRecovery", () => {
  beforeEach(() => {
    setCurrentWorkspace("test-account", "workspace-1");
    localStorage.clear();
    useIssueCreatePendingStore.getState().setPendingCreate();
  });

  it("keeps the exact request and key after an unknown outcome", async () => {
    const createIssue = vi.fn().mockRejectedValue(
      new ApiTransportError("POST /api/issues", true, new Error("lost")),
    );

    await expect(createIssueWithRecovery({ title: "Child" }, { createIssue }))
      .rejects.toBeInstanceOf(ApiTransportError);
    const pending = useIssueCreatePendingStore.getState().pendingCreate;
    expect(pending?.request).toEqual({ title: "Child" });
    expect(pending?.requestKey).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("recovers an older pending request before creating a different one", async () => {
    useIssueCreatePendingStore.getState().setPendingCreate({
      requestKey: "10000000-0000-4000-8000-000000000001",
      request: { title: "Earlier" },
    });
    const createIssue = vi.fn()
      .mockResolvedValueOnce(issue("1", "Earlier"))
      .mockResolvedValueOnce(issue("2", "Current"));
    const client = { createIssue };

    await expect(createIssueWithRecovery({ title: "Current" }, client))
      .resolves.toMatchObject({ id: "2" });
    expect(createIssue).toHaveBeenCalledTimes(2);
    expect(createIssue.mock.calls[0]).toEqual([
      { title: "Earlier" },
      "10000000-0000-4000-8000-000000000001",
    ]);
    expect(createIssue.mock.calls[1]?.[0]).toEqual({ title: "Current" });
    expect(useIssueCreatePendingStore.getState().pendingCreate).toBeUndefined();
  });
});
