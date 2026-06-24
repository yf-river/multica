// @vitest-environment jsdom

import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useWorkspaceAgentAvailability } from "./use-workspace-agent-availability";

const queryCalls: Array<{ queryKey: readonly unknown[]; enabled?: boolean }> = [];

vi.mock("@tanstack/react-query", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-query")>();
  return {
    ...actual,
    useQuery: (options: { queryKey: readonly unknown[]; enabled?: boolean }) => {
      queryCalls.push(options);
      return { data: undefined, isFetched: false };
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

describe("useWorkspaceAgentAvailability", () => {
  beforeEach(() => {
    queryCalls.length = 0;
  });

  it("keeps agent/member queries disabled when the caller is inactive", () => {
    const { result } = renderHook(() => useWorkspaceAgentAvailability(false));

    expect(result.current).toBe("loading");
    expect(queryCalls).toHaveLength(2);
    expect(queryCalls.map((call) => call.enabled)).toEqual([false, false]);
  });

  it("enables agent/member queries by default for active surfaces", () => {
    renderHook(() => useWorkspaceAgentAvailability());

    expect(queryCalls).toHaveLength(2);
    expect(queryCalls.map((call) => call.enabled)).toEqual([true, true]);
  });
});
