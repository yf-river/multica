// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { quickCreateIssueWithRecovery } from "./quick-create";
import { useQuickCreateStore } from "./stores/quick-create-store";

describe("quickCreateIssueWithRecovery", () => {
  beforeEach(() => {
    setCurrentWorkspace("test-workspace", "workspace-1");
    localStorage.clear();
    useQuickCreateStore.setState({ pendingOperation: null });
  });

  it("replays the original intent and key after an unknown outcome", async () => {
    const quickCreateIssue = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST /api/issues/quick-create", true, new Error("reset")))
      .mockResolvedValueOnce(undefined);
    const client = { quickCreateIssue };

    await expect(quickCreateIssueWithRecovery(client, { prompt: "Original", agent_id: "agent-1" }))
      .rejects.toBeInstanceOf(ApiTransportError);
    const pending = useQuickCreateStore.getState().pendingOperation;

    await expect(quickCreateIssueWithRecovery(client, { prompt: "Changed", agent_id: "agent-2" }))
      .resolves.toBeUndefined();
    expect(quickCreateIssue.mock.calls[1]).toEqual([
      { prompt: "Original", agent_id: "agent-1" },
      pending?.idempotencyKey,
    ]);
    expect(useQuickCreateStore.getState().pendingOperation).toBeNull();
  });

  it("releases the request identity after a definitive rejection", async () => {
    const quickCreateIssue = vi.fn()
      .mockRejectedValueOnce(new ApiError("invalid", 400, "Bad Request"))
      .mockResolvedValueOnce(undefined);
    const client = { quickCreateIssue };

    await expect(quickCreateIssueWithRecovery(client, { prompt: "First", agent_id: "agent-1" }))
      .rejects.toBeInstanceOf(ApiError);
    await quickCreateIssueWithRecovery(client, { prompt: "Second", agent_id: "agent-2" });

    expect(quickCreateIssue.mock.calls[0]?.[1]).not.toBe(quickCreateIssue.mock.calls[1]?.[1]);
    expect(quickCreateIssue.mock.calls[1]?.[0]).toEqual({ prompt: "Second", agent_id: "agent-2" });
  });
});
