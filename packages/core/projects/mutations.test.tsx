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

vi.mock("../hooks", () => ({ useWorkspaceId: () => "workspace-1" }));

import { useProjectDraftStore } from "./draft-store";
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
  useProjectDraftStore.getState().clearDraft();
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
    const retained = useProjectDraftStore.getState().draft.pendingCreate;
    const retainedKey = retained?.requestKey;
    expect(retainedKey).toBeTruthy();
    expect(retained?.request).toEqual({ title: "Roadmap" });
    const storageKey = "multica_project_draft:test-workspace";
    const persistedDraft = localStorage.getItem(storageKey);
    expect(persistedDraft).toContain(retainedKey);

    // Recreate the store state from persisted storage, as a page reload would.
    useProjectDraftStore.getState().clearDraft();
    localStorage.setItem(storageKey, persistedDraft!);
    await act(async () => {
      await useProjectDraftStore.persist.rehydrate();
    });
    expect(useProjectDraftStore.getState().draft.pendingCreate).toEqual(retained);
    first.unmount();

    const second = renderHook(() => useCreateProject(), { wrapper });
    await act(() => second.result.current.mutateAsync({ title: "Changed after reload" }));

    expect(apiMock.createProject.mock.calls[1]?.[0]).toEqual({ title: "Roadmap" });
    expect(apiMock.createProject.mock.calls[0]?.[1]).toBe(retainedKey);
    expect(apiMock.createProject.mock.calls[1]?.[1]).toBe(retainedKey);
    expect(useProjectDraftStore.getState().draft.pendingCreate).toBeUndefined();
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
