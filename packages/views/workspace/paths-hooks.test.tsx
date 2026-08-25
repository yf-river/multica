import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  WorkspaceSlugProvider,
  useWorkspaceSlug,
  useCurrentWorkspace,
} from "@multica/core/paths";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { DEFAULT_WORKSPACE_SETTINGS, type Workspace } from "@multica/core/types";

function makeWorkspace(over: Partial<Workspace>): Workspace {
  return {
    id: "id-default",
    name: "Default",
    slug: "default",
    description: null,
    context: null,
    settings: { ...DEFAULT_WORKSPACE_SETTINGS },
    repos: [],
    issue_prefix: "DEF",
    avatar_url: null,
    ...over,
  };
}

function SlugProbe() {
  return <div data-testid="slug">{useWorkspaceSlug() ?? "null"}</div>;
}

function WorkspaceProbe() {
  const workspace = useCurrentWorkspace();
  return <div data-testid="name">{workspace?.name ?? "none"}</div>;
}

function setup(slug: string | null, wsList: Workspace[] = []) {
  const qc = new QueryClient();
  qc.setQueryData(workspaceKeys.list(), wsList);
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return (
      <QueryClientProvider client={qc}>
        <WorkspaceSlugProvider slug={slug}>{children}</WorkspaceSlugProvider>
      </QueryClientProvider>
    );
  };
}

describe("useWorkspaceSlug", () => {
  it("returns the provided slug", () => {
    render(<SlugProbe />, { wrapper: setup("acme") });
    expect(screen.getByTestId("slug").textContent).toBe("acme");
  });

  it("returns null when no slug is provided", () => {
    render(<SlugProbe />, { wrapper: setup(null) });
    expect(screen.getByTestId("slug").textContent).toBe("null");
  });
});

describe("useCurrentWorkspace", () => {
  const acme = makeWorkspace({ id: "id-1", slug: "acme", name: "Acme" });

  it("resolves workspace from slug and list", () => {
    render(<WorkspaceProbe />, { wrapper: setup("acme", [acme]) });
    expect(screen.getByTestId("name").textContent).toBe("Acme");
  });

  it("returns null when slug does not match any workspace", () => {
    render(<WorkspaceProbe />, { wrapper: setup("bogus", [acme]) });
    expect(screen.getByTestId("name").textContent).toBe("none");
  });

  it("returns null when no slug is provided", () => {
    render(<WorkspaceProbe />, { wrapper: setup(null, [acme]) });
    expect(screen.getByTestId("name").textContent).toBe("none");
  });
});
