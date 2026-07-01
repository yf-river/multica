// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { WorkspaceSlugProvider } from "../paths";
import type { Workspace } from "../types";
import { workspaceKeys } from "./queries";
import { useActorName } from "./hooks";

const WORKSPACE: Workspace = {
  id: "ws-1",
  name: "测试工作区",
  slug: "ai-studio",
  description: null,
  context: null,
  settings: {},
  repos: [],
  issue_prefix: "GT",
  avatar_url: null,
  created_at: "2026-06-24T00:00:00.000Z",
  updated_at: "2026-06-24T00:00:00.000Z",
};

function makeWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  queryClient.setQueryData(workspaceKeys.list(), [WORKSPACE]);

  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        <WorkspaceSlugProvider slug={WORKSPACE.slug}>
          {children}
        </WorkspaceSlugProvider>
      </QueryClientProvider>
    );
  };
}

describe("useActorName", () => {
  it("keeps actor name callbacks stable when identity queries are disabled", () => {
    const { result, rerender } = renderHook(
      () =>
        useActorName({
          members: false,
          agents: false,
          squads: false,
        }),
      { wrapper: makeWrapper() },
    );

    const firstGetActorName = result.current.getActorName;
    const firstGetActorInitials = result.current.getActorInitials;
    const firstGetActorAvatarUrl = result.current.getActorAvatarUrl;

    rerender();

    expect(result.current.getActorName).toBe(firstGetActorName);
    expect(result.current.getActorInitials).toBe(firstGetActorInitials);
    expect(result.current.getActorAvatarUrl).toBe(firstGetActorAvatarUrl);
  });
});
