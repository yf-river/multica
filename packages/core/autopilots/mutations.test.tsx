// @vitest-environment jsdom

import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, ApiTransportError } from "../api/client";
import { setCurrentWorkspace } from "../platform/workspace-storage";

const apiMock = vi.hoisted(() => ({
  createAutopilot: vi.fn(),
  triggerAutopilot: vi.fn(),
}));

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return { ...actual, api: apiMock };
});

vi.mock("../hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

import { useCreateAutopilot, useTriggerAutopilot } from "./mutations";
import { useAutopilotPendingOperationStore } from "./pending-operation-store";

function wrapper({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider client={new QueryClient()}>
      {children}
    </QueryClientProvider>
  );
}

const createRequest = {
  title: "Daily triage",
  assignee_type: "agent" as const,
  assignee_id: "agent-1",
  execution_mode: "run_only" as const,
  trigger: { kind: "webhook" as const },
};

beforeEach(async () => {
  vi.clearAllMocks();
  setCurrentWorkspace("test-workspace", "workspace-1");
  await Promise.resolve();
  localStorage.clear();
  useAutopilotPendingOperationStore.getState().clear();
});

describe("Autopilot mutation request identity", () => {
  it("rehydrates the exact create intent after an unknown outcome", async () => {
    apiMock.createAutopilot
      .mockRejectedValueOnce(
        new ApiTransportError("POST /api/autopilots", true, new Error("reset")),
      )
      .mockResolvedValueOnce({ id: "autopilot-1" });
    const first = renderHook(() => useCreateAutopilot(), { wrapper });

    await expect(
      act(() => first.result.current.mutateAsync(createRequest)),
    ).rejects.toBeInstanceOf(ApiTransportError);
    const retained = useAutopilotPendingOperationStore.getState().pendingCreate;
    expect(retained?.request).toEqual(createRequest);
    const storageKey = "multica_autopilot_pending_operations:test-workspace";
    const persisted = localStorage.getItem(storageKey);
    expect(persisted).toContain(retained?.requestKey);

    useAutopilotPendingOperationStore.getState().clear();
    localStorage.setItem(storageKey, persisted!);
    await act(async () => {
      await useAutopilotPendingOperationStore.persist.rehydrate();
    });
    first.unmount();

    const second = renderHook(() => useCreateAutopilot(), { wrapper });
    await act(() => second.result.current.mutateAsync({
      ...createRequest,
      title: "Changed after reload",
    }));

    expect(apiMock.createAutopilot).toHaveBeenCalledTimes(2);
    expect(apiMock.createAutopilot.mock.calls[1]?.[0]).toEqual(createRequest);
    expect(apiMock.createAutopilot.mock.calls[0]?.[1]).toBe(
      apiMock.createAutopilot.mock.calls[1]?.[1],
    );
    expect(useAutopilotPendingOperationStore.getState().pendingCreate).toBeUndefined();
  });

  it("releases the create key after a definitive rejection", async () => {
    apiMock.createAutopilot
      .mockRejectedValueOnce(new ApiError("invalid", 400, "Bad Request"))
      .mockResolvedValueOnce({ id: "autopilot-1" });
    const { result } = renderHook(() => useCreateAutopilot(), { wrapper });

    await expect(
      act(() => result.current.mutateAsync(createRequest)),
    ).rejects.toBeInstanceOf(ApiError);
    await act(() => result.current.mutateAsync(createRequest));

    expect(apiMock.createAutopilot.mock.calls[0]?.[1]).not.toBe(
      apiMock.createAutopilot.mock.calls[1]?.[1],
    );
  });

  it("rehydrates a manual-trigger key per Autopilot after an unknown outcome", async () => {
    apiMock.triggerAutopilot
      .mockRejectedValueOnce(
        new ApiTransportError(
          "POST /api/autopilots/:id/trigger",
          true,
          new Error("reset"),
        ),
      )
      .mockResolvedValueOnce({ id: "run-1" });
    const first = renderHook(() => useTriggerAutopilot(), { wrapper });

    await expect(
      act(() => first.result.current.mutateAsync("autopilot-1")),
    ).rejects.toBeInstanceOf(ApiTransportError);
    const retainedKey = useAutopilotPendingOperationStore.getState()
      .manualTriggerKeys["autopilot-1"];
    const storageKey = "multica_autopilot_pending_operations:test-workspace";
    const persisted = localStorage.getItem(storageKey);
    expect(persisted).toContain(retainedKey);

    useAutopilotPendingOperationStore.getState().clear();
    localStorage.setItem(storageKey, persisted!);
    await act(async () => {
      await useAutopilotPendingOperationStore.persist.rehydrate();
    });
    first.unmount();

    const second = renderHook(() => useTriggerAutopilot(), { wrapper });
    await act(() => second.result.current.mutateAsync("autopilot-1"));

    expect(apiMock.triggerAutopilot.mock.calls[0]?.[1]).toBe(
      apiMock.triggerAutopilot.mock.calls[1]?.[1],
    );
    expect(useAutopilotPendingOperationStore.getState()
      .manualTriggerKeys["autopilot-1"]).toBeUndefined();
  });
});
