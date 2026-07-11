// @vitest-environment jsdom

import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, ApiTransportError } from "../api/client";
import { setCurrentWorkspace } from "../platform/workspace-storage";

const apiMock = vi.hoisted(() => ({ createSquad: vi.fn() }));

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return { ...actual, api: apiMock };
});

vi.mock("../hooks", () => ({ useWorkspaceId: () => "workspace-1" }));

import { useCreateSquad } from "./mutations";
import { useSquadPendingOperationStore } from "./pending-operation-store";

function wrapper({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider client={new QueryClient()}>
      {children}
    </QueryClientProvider>
  );
}

const createRequest = {
  name: "Platform",
  leader_id: "agent-1",
  scope: "workspace" as const,
  members: [{ member_type: "agent" as const, member_id: "agent-2" }],
};

beforeEach(async () => {
  vi.clearAllMocks();
  setCurrentWorkspace("test-workspace", "workspace-1");
  await Promise.resolve();
  localStorage.clear();
  useSquadPendingOperationStore.getState().clear();
});

describe("Squad create request identity", () => {
  it("rehydrates the exact create intent after an unknown outcome", async () => {
    apiMock.createSquad
      .mockRejectedValueOnce(
        new ApiTransportError("POST /api/squads", true, new Error("reset")),
      )
      .mockResolvedValueOnce({ id: "squad-1" });
    const first = renderHook(() => useCreateSquad(), { wrapper });

    await expect(
      act(() => first.result.current.mutateAsync(createRequest)),
    ).rejects.toBeInstanceOf(ApiTransportError);
    const retained = useSquadPendingOperationStore.getState().pendingCreate;
    expect(retained?.request).toEqual(createRequest);
    const storageKey = "multica_squad_pending_operations:test-workspace";
    const persisted = localStorage.getItem(storageKey);
    expect(persisted).toContain(retained?.requestKey);

    useSquadPendingOperationStore.getState().clear();
    localStorage.setItem(storageKey, persisted!);
    await act(async () => {
      await useSquadPendingOperationStore.persist.rehydrate();
    });
    first.unmount();

    const second = renderHook(() => useCreateSquad(), { wrapper });
    await act(() => second.result.current.mutateAsync({
      ...createRequest,
      name: "Changed after reload",
    }));

    expect(apiMock.createSquad).toHaveBeenCalledTimes(2);
    expect(apiMock.createSquad.mock.calls[1]?.[0]).toEqual(createRequest);
    expect(apiMock.createSquad.mock.calls[0]?.[1]).toBe(
      apiMock.createSquad.mock.calls[1]?.[1],
    );
    expect(useSquadPendingOperationStore.getState().pendingCreate).toBeUndefined();
  });

  it("releases the create key after a definitive rejection", async () => {
    apiMock.createSquad
      .mockRejectedValueOnce(new ApiError("invalid", 400, "Bad Request"))
      .mockResolvedValueOnce({ id: "squad-1" });
    const { result } = renderHook(() => useCreateSquad(), { wrapper });

    await expect(
      act(() => result.current.mutateAsync(createRequest)),
    ).rejects.toBeInstanceOf(ApiError);
    await act(() => result.current.mutateAsync(createRequest));

    expect(apiMock.createSquad.mock.calls[0]?.[1]).not.toBe(
      apiMock.createSquad.mock.calls[1]?.[1],
    );
  });
});
