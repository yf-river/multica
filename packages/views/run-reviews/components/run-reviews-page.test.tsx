// @vitest-environment jsdom

import { screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AnchorHTMLAttributes } from "react";
import type { Issue, IssueExecutionTreeResponse } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mockState = vi.hoisted(() => ({
  listIssues: vi.fn(),
  getExecutionTree: vi.fn(),
  listTasks: vi.fn(),
  rerunIssue: vi.fn(),
}));

vi.mock("@multica/core/issues/queries", () => ({
  issueListOptions: () => ({
    queryKey: ["issues", "list"],
    queryFn: mockState.listIssues,
  }),
  issueExecutionTreeOptions: (issueId: string) => ({
    queryKey: ["issues", "execution-tree", issueId],
    queryFn: () => mockState.getExecutionTree(issueId),
  }),
  issueKeys: {
    tasks: (issueId: string) => ["issues", "tasks", issueId],
    executionTree: (issueId: string) => ["issues", "execution-tree", issueId],
    list: (workspaceId: string) => ["issues", "list", workspaceId],
    detail: (workspaceId: string, issueId: string) => ["issues", "detail", workspaceId, issueId],
  },
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listTasksByIssue: mockState.listTasks,
    rerunIssue: mockState.rerunIssue,
  },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    runReviews: () => "/acme/run-reviews",
    issueDetail: (issueId: string) => `/acme/issues/${issueId}`,
    evaluationView: (view: string) => `/acme/evaluation/${view}`,
  }),
}));

vi.mock("@multica/core/realtime", () => ({
  useWSEvent: vi.fn(),
  useWSReconnect: vi.fn(),
}));

vi.mock("../../navigation", () => ({
  AppLink: ({ href, children, ...props }: AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a href={href} {...props}>{children}</a>
  ),
  useNavigation: () => ({ searchParams: new URLSearchParams() }),
}));

vi.mock("../../common/task-transcript", () => ({
  TranscriptButton: () => null,
}));

import { RunReviewsPage } from "./run-reviews-page";

const issue = {
  id: "issue-1",
  identifier: "MUL-1",
  title: "登录失败复盘",
  status: "in_progress",
  project: { id: "project-1", title: "账号平台" },
  child_progress: { done: 0, total: 0 },
  agent_activity: { running_count: 0, queued_count: 0 },
} as Issue;

const executionTree = {
  root: {
    issue,
    tasks: [],
    sop_runs: [],
    task_messages: [],
    trace_events: [],
    tool_call_chains: [],
    tool_call_summary: [],
    wakeup_comments: [],
    children: [],
  },
  summary: {},
  timeline_nodes: [],
} satisfies IssueExecutionTreeResponse;

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <RunReviewsPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockState.listIssues.mockResolvedValue([]);
  mockState.getExecutionTree.mockResolvedValue(executionTree);
  mockState.listTasks.mockResolvedValue([]);
});

describe("RunReviewsPage", () => {
  it("renders the current empty state without historical fixtures", async () => {
    renderPage();

    expect(screen.getByRole("heading", { name: "运行复盘" })).toBeInTheDocument();
    expect(await screen.findByText("暂无 issue。请先通过公开 UI/API 创建任务。")).toBeInTheDocument();
    expect(screen.getByText("选择一条 issue 查看完整链路。")).toBeInTheDocument();
  });

  it("orchestrates the selected issue review sections from the execution tree", async () => {
    mockState.listIssues.mockResolvedValue([issue]);
    renderPage();

    expect(await screen.findByRole("heading", { name: "登录失败复盘" })).toBeInTheDocument();
    expect(screen.getByText("项目：账号平台")).toBeInTheDocument();
    expect(screen.getByText("任务：无运行任务")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "生成评测用例" })).toBeEnabled();
    expect(screen.getByText("横向时序图")).toBeInTheDocument();
    expect(screen.getByText("节点表")).toBeInTheDocument();
    expect(screen.getByText("事件流")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("搜索事件、Agent、工具、结果或 task")).toBeInTheDocument();
  });
});
