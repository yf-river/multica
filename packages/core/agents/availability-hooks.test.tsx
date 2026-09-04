// @vitest-environment jsdom

import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useAgentPresenceDetail } from "./use-agent-presence";
import { useWorkspaceAgentAvailability } from "./use-workspace-agent-availability";

const queryCalls: Array<{ queryKey: readonly unknown[]; enabled?: boolean }> = [];

vi.mock("@tanstack/react-query", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-query")>();
  return {
    ...actual,
    useQuery: (options: { queryKey: readonly unknown[]; enabled?: boolean }) => {
      queryCalls.push(options);
      return { data: undefined, isError: false, isFetched: false };
    },
  };
});

vi.mock("../hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("../auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));

beforeEach(() => {
  queryCalls.length = 0;
});

describe("Agent availability query enablement", () => {
  it.each([
    ["presence inactive", () => useAgentPresenceDetail("ws-1", "agent-1", false), [false, false, false], true],
    ["presence active", () => useAgentPresenceDetail("ws-1", "agent-1"), [true, true, true], false],
    ["workspace inactive", () => useWorkspaceAgentAvailability(false), [false, false], true],
    ["workspace active", () => useWorkspaceAgentAvailability(), [true, true], false],
  ] as const)("uses current query state for %s", (_name, useHook, expected, loading) => {
    const { result } = renderHook((): unknown => useHook());

    if (loading) expect(result.current).toBe("loading");
    expect(queryCalls.map((call) => call.enabled)).toEqual(expected);
  });
});
