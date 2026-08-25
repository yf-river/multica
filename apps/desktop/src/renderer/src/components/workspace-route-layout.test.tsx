import { describe, expect, it, vi, beforeEach } from "vitest";
import { render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

// vi.hoisted shared state for all the stores / hooks the layout consumes.
const state = vi.hoisted(() => ({
  user: null as { id: string } | null,
  isAuthLoading: false,
  workspace: null as { id: string; slug: string } | null,
  listFetched: true,
  wsList: [] as { id: string; slug: string }[],
  workspaceSeen: true,
}));

vi.mock("@multica/core/auth", () => {
  const useAuthStore = (selector: (s: typeof state) => unknown) => {
    if (selector.toString().includes("isLoading"))
      return state.isAuthLoading;
    return state.user;
  };
  return { useAuthStore };
});

vi.mock("@multica/core/platform", () => ({
  setCurrentWorkspace: vi.fn(),
}));

vi.mock("@multica/core/workspace", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/workspace")>(
    "@multica/core/workspace",
  );
  return {
    ...actual,
    workspaceBySlugOptions: () => ({
      queryKey: ["workspace-by-slug"],
      queryFn: async () => state.workspace,
    }),
    workspaceListOptions: () => ({
      queryKey: ["workspace-list"],
      queryFn: async () => state.wsList,
    }),
  };
});

vi.mock("@multica/core/paths", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/paths")>(
    "@multica/core/paths",
  );
  return {
    ...actual,
    WorkspaceSlugProvider: ({ children }: { children: React.ReactNode }) => (
      <>{children}</>
    ),
    paths: {
      ...actual.paths,
      login: () => "/login",
    },
  };
});

vi.mock("@multica/views/workspace/use-workspace-seen", () => ({
  useWorkspaceSeen: () => state.workspaceSeen,
}));

vi.mock("@multica/views/layout", () => ({
  WorkspacePresencePrefetch: () => null,
}));

vi.mock("@/stores/tab-store", () => ({
  useTabStore: Object.assign(() => null, {
    getState: () => ({ validateWorkspaceSlugs: vi.fn() }),
  }),
}));

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { WorkspaceRouteLayout } from "./workspace-route-layout";

function renderLayout() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  // Seed the workspace queries so the gate inside the layout passes
  // synchronously — the real hook reads from cache.
  qc.setQueryData(["workspace-by-slug"], state.workspace);
  qc.setQueryData(["workspace-list"], state.wsList);
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={["/acme/issues"]}>
        <Routes>
          <Route path=":workspaceSlug/*" element={<WorkspaceRouteLayout />}>
            <Route path="*" element={<div data-testid="outlet" />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  state.user = { id: "u1" };
  state.isAuthLoading = false;
  state.workspace = { id: "ws-1", slug: "acme" };
  state.listFetched = true;
  state.wsList = [{ id: "ws-1", slug: "acme" }];
  state.workspaceSeen = true;
});

describe("WorkspaceRouteLayout", () => {
  it("renders the workspace outlet", () => {
    const { queryByTestId } = renderLayout();
    expect(queryByTestId("outlet")).not.toBeNull();
  });
});
