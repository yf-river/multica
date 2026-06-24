// @vitest-environment jsdom

import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useAgentPresenceDetail } from "./use-agent-presence";

const queryCalls: Array<{ queryKey: readonly unknown[]; enabled?: boolean }> = [];

vi.mock("@tanstack/react-query", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-query")>();
  return {
    ...actual,
    useQuery: (options: { queryKey: readonly unknown[]; enabled?: boolean }) => {
      queryCalls.push(options);
      return { data: undefined, isError: false };
    },
  };
});

describe("useAgentPresenceDetail", () => {
  beforeEach(() => {
    queryCalls.length = 0;
  });

  it("keeps all presence queries disabled when the caller is inactive", () => {
    const { result } = renderHook(() => useAgentPresenceDetail("ws-1", "agent-1", false));

    expect(result.current).toBe("loading");
    expect(queryCalls).toHaveLength(3);
    expect(queryCalls.map((call) => call.enabled)).toEqual([false, false, false]);
  });

  it("enables all presence queries by default for active surfaces", () => {
    renderHook(() => useAgentPresenceDetail("ws-1", "agent-1"));

    expect(queryCalls).toHaveLength(3);
    expect(queryCalls.map((call) => call.enabled)).toEqual([true, true, true]);
  });
});
