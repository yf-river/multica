// @vitest-environment jsdom

import { screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { renderWithI18n } from "../../test/i18n";

const mockApi = vi.hoisted(() => ({
  listPromptLibraryItems: vi.fn(),
  listAgents: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listPromptLibraryItems: mockApi.listPromptLibraryItems,
    listAgents: mockApi.listAgents,
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
    pathname: "/acme/debug/prompts",
    searchParams: new URLSearchParams(),
  }),
}));

import { PromptLibraryPage } from "./prompt-library-page";

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <PromptLibraryPage activeView="prompts" />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockApi.listPromptLibraryItems.mockResolvedValue({ items: [], total: 0 });
  mockApi.listAgents.mockResolvedValue([]);
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
});
