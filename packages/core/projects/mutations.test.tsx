// @vitest-environment jsdom

import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, ApiTransportError } from "../api/client";
import { setCurrentWorkspace } from "../platform/workspace-storage";

const apiMock = vi.hoisted(() => ({ createProject: vi.fn() }));

vi.mock("../api", async () => {
  const actual = await vi.importActual<typeof import("../api")>("../api");
  return { ...actual, api: apiMock };
});

vi.mock("../paths", () => ({ useWorkspaceId: () => "workspace-1" }));

import { useProjectCreateOperationStore } from "./draft-store";
import { useCreateProject } from "./mutations";

function wrapper({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider client={new QueryClient()}>
      {children}
    </QueryClientProvider>
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  setCurrentWorkspace("test-workspace", "workspace-1");
  await Promise.resolve();
  localStorage.clear();
  useProjectCreateOperationStore.getState().setPending();
});

describe("Project create request identity", () => {
  it("retains the request key across an unknown outcome and hook remount", async () => {
    apiMock.createProject
      .mockRejectedValueOnce(
        new ApiTransportError("POST /api/projects", true, new Error("reset")),
      )
      .mockResolvedValueOnce({ id: "project-1" });
    const first = renderHook(() => useCreateProject(), { wrapper });

    await expect(
      act(() => first.result.current.mutateAsync({ title: "Roadmap" })),
    ).rejects.toBeInstanceOf(ApiTransportError);
    const retained = useProjectCreateOperationStore.getState().pending;
    const retainedKey = retained?.requestKey;
    expect(retainedKey).toBeTruthy();
    expect(retained?.request).toEqual({ title: "Roadmap" });
    const storageKey = "multica_project_create_operation:test-workspace";
    const persistedOperation = localStorage.getItem(storageKey);
    expect(persistedOperation).toContain(retainedKey);

    // Recreate the store state from persisted storage, as a page reload would.
    useProjectCreateOperationStore.getState().setPending();
    localStorage.setItem(storageKey, persistedOperation!);
    await act(async () => {
      await useProjectCreateOperationStore.persist.rehydrate();
    });
    expect(useProjectCreateOperationStore.getState().pending).toEqual(retained);
    first.unmount();

    const second = renderHook(() => useCreateProject(), { wrapper });
    await act(() => second.result.current.mutateAsync({ title: "Changed after reload" }));

    expect(apiMock.createProject.mock.calls[1]?.[0]).toEqual({ title: "Roadmap" });
    expect(apiMock.createProject.mock.calls[0]?.[1]).toBe(retainedKey);
    expect(apiMock.createProject.mock.calls[1]?.[1]).toBe(retainedKey);
    expect(useProjectCreateOperationStore.getState().pending).toBeUndefined();
  });

  it("releases the key after a definitive rejection", async () => {
    apiMock.createProject
      .mockRejectedValueOnce(new ApiError("invalid", 400, "Bad Request"))
      .mockResolvedValueOnce({ id: "project-1" });
    const { result } = renderHook(() => useCreateProject(), { wrapper });

    await expect(
      act(() => result.current.mutateAsync({ title: "" })),
    ).rejects.toBeInstanceOf(ApiError);
    await act(() => result.current.mutateAsync({ title: "Roadmap" }));

    expect(apiMock.createProject.mock.calls[0]?.[1]).not.toBe(
      apiMock.createProject.mock.calls[1]?.[1],
    );
  });
});
