// @vitest-environment jsdom

import type { PropsWithChildren } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Workspace } from "../types";
import { workspaceKeys } from "./queries";
import { useCreateWorkspace } from "./mutations";

const mocks = vi.hoisted(() => ({ createWorkspaceWithRecovery: vi.fn() }));

vi.mock("./create-operation", () => ({
  createWorkspaceWithRecovery: mocks.createWorkspaceWithRecovery,
}));

const workspace = {
  id: "workspace-1",
  name: "Recovered",
  slug: "recovered",
} as Workspace;

describe("useCreateWorkspace", () => {
  beforeEach(() => vi.clearAllMocks());

  it("replaces a recovered workspace already present in the list cache", async () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(workspaceKeys.list(), [{ ...workspace, name: "Stale" }]);
    mocks.createWorkspaceWithRecovery.mockResolvedValue(workspace);
    const wrapper = ({ children }: PropsWithChildren) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(() => useCreateWorkspace(), { wrapper });

    await act(() => result.current.mutateAsync({ name: workspace.name, slug: workspace.slug }));

    expect(queryClient.getQueryData(workspaceKeys.list())).toEqual([workspace]);
  });
});
