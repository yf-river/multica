// @vitest-environment jsdom

import { screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { renderWithI18n } from "../../test/i18n";

const mockApi = vi.hoisted(() => ({
  listPromptLibraryItems: vi.fn(),
  listAgents: vi.fn(),
  listPromptEvaluationAssets: vi.fn(),
  listPromptEvaluationCases: vi.fn(),
  getIssueExecutionTree: vi.fn(),
}));

const navigationState = vi.hoisted(() => ({
  pathname: "/acme/debug/prompts",
  search: "",
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listPromptLibraryItems: mockApi.listPromptLibraryItems,
    listAgents: mockApi.listAgents,
    listPromptEvaluationAssets: mockApi.listPromptEvaluationAssets,
    listPromptEvaluationCases: mockApi.listPromptEvaluationCases,
    getIssueExecutionTree: mockApi.getIssueExecutionTree,
  },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    runReviews: () => "/acme/run-reviews",
    issueDetail: (issueId: string) => `/acme/issues/${issueId}`,
  }),
}));

vi.mock("../../navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: ReactNode }) => <a href={href}>{children}</a>,
  useNavigation: () => ({
    pathname: navigationState.pathname,
    searchParams: new URLSearchParams(navigationState.search),
  }),
}));

import { PromptLibraryPage } from "./prompt-library-page";

function renderPage(activeView: "prompts" | "datasets" = "prompts") {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <PromptLibraryPage activeView={activeView} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  navigationState.pathname = "/acme/debug/prompts";
  navigationState.search = "";
  mockApi.listPromptLibraryItems.mockResolvedValue({ items: [], total: 0 });
  mockApi.listAgents.mockResolvedValue([]);
  mockApi.listPromptEvaluationAssets.mockResolvedValue({ items: [], total: 0 });
  mockApi.listPromptEvaluationCases.mockResolvedValue({ items: [], total: 0 });
  mockApi.getIssueExecutionTree.mockResolvedValue({
    root: { id: "issue-1", tasks: [{ id: "task-1" }], children: [] },
  });
});

describe("PromptLibraryPage prompt editor orchestration", () => {
  it("renders the current empty prompt library and new-prompt editor", async () => {
    renderPage();

    expect(await screen.findByText("暂无提示词")).toBeInTheDocument();
    expect(screen.getByText("当前调试子模块：提示词库")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "新建" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "新建提示词" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "创建提示词" })).toBeInTheDocument();
    expect(screen.getByText("保存后会生成第一个不可变版本记录。")).toBeInTheDocument();
  });

  it("loads the focused issue task tree for trace-backed dataset imports", async () => {
    navigationState.pathname = "/acme/evaluation/datasets";
    navigationState.search = "issue=issue-1";

    renderPage("datasets");

    expect(await screen.findByText("暂无数据集，可以先新建一个评估数据集。")).toBeInTheDocument();
    expect(mockApi.getIssueExecutionTree).toHaveBeenCalledWith("issue-1");
  });
});
