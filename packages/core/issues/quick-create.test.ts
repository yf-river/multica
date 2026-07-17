// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError, ApiTransportError, getApi, setApiInstance } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { quickCreateIssueWithRecovery } from "./quick-create";
import { useQuickCreateStore } from "./stores/quick-create-store";

describe("quickCreateIssueWithRecovery", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setApiInstance(new ApiClient("http://core.test"));
    setCurrentWorkspace("test-workspace", "workspace-1");
    localStorage.clear();
    useQuickCreateStore.setState({ pending: undefined });
  });

  it("replays the original intent and key after an unknown outcome", async () => {
    const quickCreateIssue = vi.spyOn(getApi(), "quickCreateIssue")
      .mockRejectedValueOnce(new ApiTransportError("POST /api/issues/quick-create", true, new Error("reset")))
      .mockResolvedValueOnce(undefined);
    await expect(quickCreateIssueWithRecovery({ prompt: "Original", agent_id: "agent-1" }))
      .rejects.toBeInstanceOf(ApiTransportError);
    const pending = useQuickCreateStore.getState().pending;

    await expect(quickCreateIssueWithRecovery({ prompt: "Changed", agent_id: "agent-2" }))
      .resolves.toBeUndefined();
    expect(quickCreateIssue.mock.calls[1]).toEqual([
      { prompt: "Original", agent_id: "agent-1" },
      pending?.requestKey,
    ]);
    expect(useQuickCreateStore.getState().pending).toBeUndefined();
  });

  it("releases the request identity after a definitive rejection", async () => {
    const quickCreateIssue = vi.spyOn(getApi(), "quickCreateIssue")
      .mockRejectedValueOnce(new ApiError("invalid", 400, "Bad Request"))
      .mockResolvedValueOnce(undefined);
    await expect(quickCreateIssueWithRecovery({ prompt: "First", agent_id: "agent-1" }))
      .rejects.toBeInstanceOf(ApiError);
    await quickCreateIssueWithRecovery({ prompt: "Second", agent_id: "agent-2" });

    expect(quickCreateIssue.mock.calls[0]?.[1]).not.toBe(quickCreateIssue.mock.calls[1]?.[1]);
    expect(quickCreateIssue.mock.calls[1]?.[0]).toEqual({ prompt: "Second", agent_id: "agent-2" });
  });

  it("does not clear a newer pending request when an older request completes", async () => {
    let completeRequest!: () => void;
    const quickCreateIssue = vi.spyOn(getApi(), "quickCreateIssue").mockImplementation(() => new Promise<void>((resolve) => {
      completeRequest = resolve;
    }));

    const inFlight = quickCreateIssueWithRecovery({ prompt: "First", agent_id: "agent-1" });
    await vi.waitFor(() => expect(quickCreateIssue).toHaveBeenCalledOnce());

    const newer = {
      requestKey: "newer-key",
      request: { prompt: "Second", agent_id: "agent-2" },
      createdAt: Date.now(),
    };
    useQuickCreateStore.getState().setPending(newer);
    completeRequest();
    await inFlight;

    expect(useQuickCreateStore.getState().pending).toEqual(newer);
  });
});
