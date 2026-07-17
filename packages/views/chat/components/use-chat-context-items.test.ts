/**
 * @vitest-environment jsdom
 */
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setApiInstance, type ApiClient } from "@multica/core/api";
import type { Issue, Project } from "@multica/core/types";
import { useChatContextItems } from "./use-chat-context-items";

const navigation = vi.hoisted(() => ({
  pathname: "/acme/inbox",
  search: "",
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    pathname: navigation.pathname,
    searchParams: new URLSearchParams(navigation.search),
  }),
}));

vi.mock("@multica/core/chat", () => ({
  selectRecentContexts: () => undefined,
  useRecentContextStore: () => [],
}));

const issue = {
  id: "issue-1",
  identifier: "MUL-1",
  title: "Current issue",
  status: "in_progress",
} as Issue;
const project = {
  id: "project-1",
  title: "Current project",
  description: "Project context",
  icon: null,
  status: "in_progress",
} as Project;

function renderContextItems() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client: queryClient }, children);
  }
  return renderHook(() => useChatContextItems("ws-1"), { wrapper: Wrapper });
}

afterEach(() => {
  setApiInstance(undefined as unknown as ApiClient);
});

describe("useChatContextItems", () => {
  it.each([
    ["/acme/issues/issue-1", "", "issue", "issue-1", "MUL-1"],
    ["/acme/projects/project-1", "", "project", "project-1", "Current project"],
    ["/acme/inbox", "issue=issue-1", "issue", "issue-1", "MUL-1"],
  ] as const)("hydrates the current context from %s", async (pathname, search, type, id, label) => {
    navigation.pathname = pathname;
    navigation.search = search;
    setApiInstance({
      getIssue: vi.fn().mockResolvedValue(issue),
      getProject: vi.fn().mockResolvedValue(project),
    } as unknown as ApiClient);

    const { result } = renderContextItems();

    await waitFor(() => {
      expect(result.current[0]).toMatchObject({ type, id, label, group: "current" });
    });
  });

  it("does not invent current context for the bare inbox route", () => {
    navigation.pathname = "/acme/inbox";
    navigation.search = "";
    setApiInstance({} as ApiClient);

    const { result } = renderContextItems();

    expect(result.current).toEqual([]);
  });
});
