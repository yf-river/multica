import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent, AgentRuntime } from "@multica/core/types/agent";

const mocks = vi.hoisted(() => ({
  getAgent: vi.fn(),
  listRuntimes: vi.fn(),
  warn: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    getAgent: mocks.getAgent,
    listRuntimes: mocks.listRuntimes,
  },
}));

vi.mock("@multica/core/logger", () => ({
  createLogger: () => ({ warn: mocks.warn }),
}));

import { useTranscriptMetadata } from "./use-transcript-metadata";

const agentA = { id: "agent-a", description: "Agent A" } as Agent;
const runtimeA = { id: "runtime-a", name: "Runtime A" } as AgentRuntime;

describe("useTranscriptMetadata", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.getAgent.mockResolvedValue(agentA);
    mocks.listRuntimes.mockResolvedValue([runtimeA]);
  });

  it("never exposes metadata loaded for a previous task identity", async () => {
    const { result, rerender } = renderHook(
      ({ agentId, runtimeId }) => useTranscriptMetadata(true, agentId, runtimeId),
      { initialProps: { agentId: "agent-a", runtimeId: "runtime-a" } },
    );

    await waitFor(() => {
      expect(result.current).toEqual({ agentInfo: agentA, runtimeInfo: runtimeA });
    });

    mocks.getAgent.mockRejectedValueOnce(new Error("agent unavailable"));
    mocks.listRuntimes.mockRejectedValueOnce(new Error("runtime unavailable"));
    rerender({ agentId: "agent-b", runtimeId: "runtime-b" });

    expect(result.current).toEqual({ agentInfo: null, runtimeInfo: null });
    await waitFor(() => {
      expect(mocks.warn).toHaveBeenCalledTimes(2);
    });
    expect(result.current).toEqual({ agentInfo: null, runtimeInfo: null });
  });

  it("ignores a previous task request that resolves after identity changes", async () => {
    let resolveAgentA: ((agent: Agent) => void) | undefined;
    mocks.getAgent
      .mockImplementationOnce(
        () =>
          new Promise<Agent>((resolve) => {
            resolveAgentA = resolve;
          }),
      )
      .mockResolvedValueOnce({ id: "agent-b", description: "Agent B" } as Agent);
    mocks.listRuntimes.mockResolvedValue([]);

    const { result, rerender } = renderHook(
      ({ agentId }) => useTranscriptMetadata(true, agentId, null),
      { initialProps: { agentId: "agent-a" } },
    );
    rerender({ agentId: "agent-b" });

    await waitFor(() => {
      expect(result.current.agentInfo?.id).toBe("agent-b");
    });
    resolveAgentA?.(agentA);
    await Promise.resolve();

    expect(result.current.agentInfo?.id).toBe("agent-b");
  });
});
