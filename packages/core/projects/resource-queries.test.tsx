/**
 * @vitest-environment jsdom
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ListProjectResourcesResponse, ProjectResource } from "../types";
import {
  projectResourceKeys,
  useCreateProjectResource,
  useDeleteProjectResource,
} from "./resource-queries";

const apiMock = vi.hoisted(() => ({
  createProjectResource: vi.fn(),
  deleteProjectResource: vi.fn(),
}));

vi.mock("../api", () => ({ api: apiMock }));

const resource: ProjectResource = {
  id: "resource-1",
  project_id: "project-1",
  workspace_id: "workspace-1",
  resource_type: "github_repo",
  resource_ref: { url: "https://example.test/repo" },
  label: null,
  position: 0,
  created_at: "2026-07-11T00:00:00Z",
  created_by: "user-1",
};

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  };
}

describe("project resource mutations", () => {
  let queryClient: QueryClient;
  let invalidateSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    vi.clearAllMocks();
    queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");
  });

  it("refreshes project list, detail, and resources after creation", async () => {
    apiMock.createProjectResource.mockResolvedValueOnce(resource);
    queryClient.setQueryData<ListProjectResourcesResponse>(
      projectResourceKeys.list("workspace-1", "project-1"),
      { resources: [], total: 0 },
    );
    const { result } = renderHook(
      () => useCreateProjectResource("workspace-1", "project-1"),
      { wrapper: createWrapper(queryClient) },
    );

    await act(async () => {
      await result.current.mutateAsync({
        resource_type: "github_repo",
        resource_ref: { url: "https://example.test/repo" },
      });
    });

    expect(queryClient.getQueryData<ListProjectResourcesResponse>(
      projectResourceKeys.list("workspace-1", "project-1"),
    )).toEqual({ resources: [resource], total: 1 });
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["projects", "workspace-1"],
    });
  });

  it("does not decrement a stale list when the deleted resource is absent", async () => {
    apiMock.deleteProjectResource.mockResolvedValueOnce(undefined);
    queryClient.setQueryData<ListProjectResourcesResponse>(
      projectResourceKeys.list("workspace-1", "project-1"),
      { resources: [resource], total: 1 },
    );
    const { result } = renderHook(
      () => useDeleteProjectResource("workspace-1", "project-1"),
      { wrapper: createWrapper(queryClient) },
    );

    await act(async () => {
      await result.current.mutateAsync("resource-missing");
    });

    expect(queryClient.getQueryData<ListProjectResourcesResponse>(
      projectResourceKeys.list("workspace-1", "project-1"),
    )).toEqual({ resources: [resource], total: 1 });
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["projects", "workspace-1"],
    });
  });
});
