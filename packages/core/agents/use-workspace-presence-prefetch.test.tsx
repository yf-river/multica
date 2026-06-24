/* @vitest-environment jsdom */

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  useWorkspacePresencePrefetch,
  WORKSPACE_PRESENCE_PREFETCH_DELAY_MS,
} from "./use-workspace-presence-prefetch";

const { mockUseQuery } = vi.hoisted(() => ({
  mockUseQuery: vi.fn((opts: { enabled?: boolean }) => ({ data: undefined, ...opts })),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQuery: mockUseQuery,
  };
});

describe("useWorkspacePresencePrefetch", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mockUseQuery.mockClear();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("首屏后再启用 presence 预取", () => {
    renderHook(() => useWorkspacePresencePrefetch("ws-1"));

    expect(enabledCalls()).toEqual([false, false, false, false]);

    act(() => {
      vi.advanceTimersByTime(WORKSPACE_PRESENCE_PREFETCH_DELAY_MS - 1);
    });
    expect(enabledCalls()).toEqual([false, false, false, false]);

    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(enabledCalls().slice(-4)).toEqual([true, true, true, true]);
  });

  it("工作区切换后重新等待延迟窗口", () => {
    const { rerender } = renderHook(
      ({ wsId }) => useWorkspacePresencePrefetch(wsId),
      { initialProps: { wsId: "ws-1" as string | undefined } },
    );

    act(() => {
      vi.advanceTimersByTime(WORKSPACE_PRESENCE_PREFETCH_DELAY_MS);
    });
    expect(enabledCalls().slice(-4)).toEqual([true, true, true, true]);

    rerender({ wsId: "ws-2" });
    expect(enabledCalls().slice(-4)).toEqual([false, false, false, false]);

    act(() => {
      vi.advanceTimersByTime(WORKSPACE_PRESENCE_PREFETCH_DELAY_MS);
    });
    expect(enabledCalls().slice(-4)).toEqual([true, true, true, true]);
  });
});

function enabledCalls() {
  return mockUseQuery.mock.calls.map(([opts]) => opts.enabled);
}
