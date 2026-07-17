// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError, ApiTransportError, getApi, setApiInstance } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { Agent } from "../types";
import { createAgentWithRecovery } from "./create";

const agent = { id: "agent-1" } as Agent;

describe("createAgentWithRecovery", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setApiInstance(new ApiClient("http://core.test"));
    setCurrentWorkspace("test-workspace", "workspace-1");
    localStorage.clear();
  });

  it("replays the original intent and key after an unknown outcome", async () => {
    const createAgent = vi.spyOn(getApi(), "createAgent")
      .mockRejectedValueOnce(new ApiTransportError("POST /api/agents", true, new Error("reset")))
      .mockResolvedValueOnce(agent);
    await expect(createAgentWithRecovery({ name: "Original", runtime_id: "runtime-1" }))
      .rejects.toBeInstanceOf(ApiTransportError);
    const requestKey = createAgent.mock.calls[0]?.[1];

    await expect(createAgentWithRecovery({ name: "Changed", runtime_id: "runtime-2" }))
      .resolves.toBe(agent);
    expect(createAgent.mock.calls[1]).toEqual([
      { name: "Original", runtime_id: "runtime-1" },
      requestKey,
    ]);
  });

  it("releases the key after a definitive rejection", async () => {
    const createAgent = vi.spyOn(getApi(), "createAgent")
      .mockRejectedValueOnce(new ApiError("invalid", 400, "Bad Request"))
      .mockResolvedValueOnce(agent);
    await expect(createAgentWithRecovery({ name: "First", runtime_id: "runtime-1" }))
      .rejects.toBeInstanceOf(ApiError);
    await createAgentWithRecovery({ name: "Second", runtime_id: "runtime-2" });

    expect(createAgent.mock.calls[0]?.[1]).not.toBe(createAgent.mock.calls[1]?.[1]);
    expect(createAgent.mock.calls[1]?.[0]).toEqual({ name: "Second", runtime_id: "runtime-2" });
  });
});
