// @vitest-environment jsdom

import { cleanup, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactElement } from "react";
import type { AgentTask } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mockState = vi.hoisted(() => ({
  taskMessagesOptions: vi.fn(),
  listTasksByIssue: vi.fn(),
  listIssueTaskTraceEvents: vi.fn(),
  getIssueExecutionTree: vi.fn(),
  listIssueSOPRuns: vi.fn(),
  cancelTask: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listTasksByIssue: mockState.listTasksByIssue,
    listIssueTaskTraceEvents: mockState.listIssueTaskTraceEvents,
    getIssueExecutionTree: mockState.getIssueExecutionTree,
    listIssueSOPRuns: mockState.listIssueSOPRuns,
    cancelTask: mockState.cancelTask,
  },
}));

vi.mock("@multica/core/chat/queries", () => ({
  taskMessagesOptions: mockState.taskMessagesOptions,
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    runReviews: () => "/test/run-reviews",
  }),
}));

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => <span data-testid="actor-avatar" />,
}));

vi.mock("../../common/task-transcript", () => ({
  TranscriptButton: ({ title }: { title?: string }) => (
    <button type="button">{title ?? "Transcript"}</button>
  ),
}));

vi.mock("./terminate-task-confirm-dialog", () => ({
  TerminateTaskConfirmDialog: () => null,
}));

import { ActiveTaskRow, ExecutionLogSection, IssueRunReviewSummaryCard } from "./execution-log-section";

function makeTask(overrides: Partial<AgentTask> = {}): AgentTask {
  return {
    id: "task-1",
    agent_id: "agent-1",
    runtime_id: "runtime-1",
    issue_id: "issue-1",
    status: "running",
    priority: 0,
    dispatched_at: null,
    started_at: "2026-06-08T08:00:00Z",
    completed_at: null,
    result: null,
    error: null,
    created_at: "2026-06-08T08:00:00Z",
    trigger_summary: "从评论启动",
    ...overrides,
  };
}

beforeEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-06-08T08:05:04Z"));
  mockState.listTasksByIssue.mockResolvedValue([]);
  mockState.listIssueTaskTraceEvents.mockResolvedValue({ events: [] });
  mockState.getIssueExecutionTree.mockResolvedValue(null);
  mockState.listIssueSOPRuns.mockResolvedValue({ items: [] });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("ActiveTaskRow", () => {
  it("renders running status as elapsed time only", () => {
    renderWithI18n(<ActiveTaskRow task={makeTask()} issueId="issue-1" />);

    expect(screen.getByText("5m 04s")).toBeInTheDocument();
    expect(screen.queryByText(/events?/i)).not.toBeInTheDocument();
    expect(screen.getByText("从评论启动")).toBeInTheDocument();
    expect(screen.getByText("查看记录")).toBeInTheDocument();
    expect(mockState.taskMessagesOptions).not.toHaveBeenCalled();
  });

  it("does not make transcript actions depend on hover-only rendering", () => {
    renderWithI18n(<ActiveTaskRow task={makeTask()} issueId="issue-1" />);

    const transcriptButton = screen.getByRole("button", { name: "查看记录" });
    const status = screen.getByText("5m 04s");

    expect(status.parentElement?.className).toContain("flex h-7");
    expect(status.parentElement?.className).toContain(
      "[@media(hover:hover)]:group-hover/execution-log-row:hidden",
    );
    expect(transcriptButton.parentElement?.className).toContain("flex h-7");
    expect(transcriptButton.parentElement?.className).toContain("[@media(hover:hover)]:hidden");
    expect(transcriptButton.parentElement?.className).toContain(
      "[@media(hover:hover)]:group-hover/execution-log-row:flex",
    );
  });
});

function renderWithQuery(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={client}>{ui}</QueryClientProvider>,
  );
}

describe("ExecutionLogSection trace", () => {
  it("renders durable macro trace evidence and filters detailed usage events", async () => {
    vi.useRealTimers();
    mockState.listTasksByIssue.mockResolvedValue([makeTask()]);
    mockState.listIssueTaskTraceEvents.mockResolvedValue({
      events: [
        {
          id: "trace-started",
          workspace_id: "workspace-1",
          task_id: "task-1",
          issue_id: "issue-1",
          agent_id: "agent-1",
          runtime_id: "runtime-1",
          squad_id: null,
          project_id: null,
          source: "issue",
          event_type: "task.started",
          event_name: "任务已开始",
          status: "running",
          attempt: 1,
          duration_ms: null,
          queue_wait_ms: null,
          run_ms: null,
          total_ms: null,
          provider: "",
          model: "",
          input_tokens: 0,
          output_tokens: 0,
          cache_read_tokens: 0,
          cache_write_tokens: 0,
          failure_reason: "",
          error_type: "",
          trigger_comment_id: null,
          autopilot_run_id: null,
          chat_session_id: null,
          metadata: {},
          created_at: "2026-06-08T08:00:00Z",
        },
        {
          id: "trace-1",
          workspace_id: "workspace-1",
          task_id: "task-1",
          issue_id: "issue-1",
          agent_id: "agent-1",
          runtime_id: "runtime-1",
          squad_id: null,
          project_id: null,
          source: "issue",
          event_type: "llm.usage_reported",
          event_name: "模型用量已上报",
          status: "running",
          attempt: 1,
          duration_ms: null,
          queue_wait_ms: null,
          run_ms: null,
          total_ms: null,
          provider: "codex",
          model: "gpt-5",
          input_tokens: 100,
          output_tokens: 40,
          cache_read_tokens: 10,
          cache_write_tokens: 0,
          failure_reason: "",
          error_type: "",
          trigger_comment_id: null,
          autopilot_run_id: null,
          chat_session_id: null,
          metadata: {},
          created_at: "2026-06-08T08:01:00Z",
        },
      ],
    });
    mockState.listIssueSOPRuns.mockResolvedValue({
      items: [
        {
          id: "run-1",
          workspace_id: "workspace-1",
          issue_id: "issue-1",
          squad_id: "squad-1",
          leader_task_id: "task-1",
          profile_key: "default",
          profile: {},
          status: "进行中",
          current_step_key: "04-implement",
          started_at: "2026-06-08T08:00:00Z",
          completed_at: null,
          total_duration_ms: null,
          metrics: {},
          events: [],
          created_at: "2026-06-08T08:00:00Z",
          updated_at: "2026-06-08T08:00:00Z",
        },
      ],
    });

    renderWithQuery(<ExecutionLogSection issueId="issue-1" />);

    expect(await screen.findByText("最近事件")).toBeInTheDocument();
    expect(screen.getByText("当前阶段")).toBeInTheDocument();
    expect(screen.getByText("04-implement")).toBeInTheDocument();
    expect(screen.getByText("Agent：1")).toBeInTheDocument();
    expect(screen.getByText("任务：1 进行中 1")).toBeInTheDocument();
    expect(screen.getByText("首次开始：2026-06-08 08:00")).toBeInTheDocument();
    expect(screen.getByText("任务已开始")).toBeInTheDocument();
    expect(screen.queryByText("模型用量")).not.toBeInTheDocument();
    expect(screen.queryByText("模型用量已上报")).not.toBeInTheDocument();
    expect(screen.queryByText(/150 tokens/)).not.toBeInTheDocument();
    expect(screen.queryByText("观测事件")).not.toBeInTheDocument();
    expect(screen.queryByText("任务事件树")).not.toBeInTheDocument();
    expect(screen.queryByText("运行复盘")).not.toBeInTheDocument();
  });

  it("keeps a completed task execution summary visible after active work ends", async () => {
    vi.useRealTimers();
    mockState.listTasksByIssue.mockResolvedValue([
      makeTask({
        status: "completed",
        dispatched_at: "2026-06-08T08:01:00Z",
        started_at: "2026-06-08T08:02:00Z",
        completed_at: "2026-06-08T08:07:00Z",
      }),
    ]);

    renderWithQuery(<ExecutionLogSection issueId="issue-1" />);

    expect(await screen.findByTestId("issue-execution-log-section")).toBeInTheDocument();
    expect(screen.getByText("Agent：1")).toBeInTheDocument();
    expect(screen.getByText("任务：1")).toBeInTheDocument();
    expect(screen.getByText("已完成：1")).toBeInTheDocument();
    expect(screen.getByText("首次领取：2026-06-08 08:01")).toBeInTheDocument();
    expect(screen.getByText("首次开始：2026-06-08 08:02")).toBeInTheDocument();
    expect(screen.getByText("最后完成：2026-06-08 08:07")).toBeInTheDocument();
    expect(screen.getByText("任务已完成")).toBeInTheDocument();
  });

  it("renders the collaboration execution tree across parent and child issues", async () => {
    vi.useRealTimers();
    mockState.getIssueExecutionTree.mockResolvedValue({
      summary: {
        任务数: 2,
        子任务数: 1,
        SOP执行数: 1,
        SOP事件数: 2,
        观测事件数: 3,
        工具调用数: 2,
        异常工具数: 1,
        唤醒评论数: 1,
        完成任务数: 1,
        失败任务数: 0,
        取消任务数: 0,
      },
      issue_summary: {
        issue_id: "issue-parent",
        node_count: 12,
        total_duration_ms: 65000,
        total_input_tokens: 100,
        total_output_tokens: 40,
        total_cache_read_tokens: 20,
        total_cache_write_tokens: 10,
        message_count: 8,
        agent_turn_count: 4,
        trace_event_count: 6,
        usage_unavailable: false,
        failure_summary: "验收失败：缺少执行级结论",
        acceptance_status: "failed",
        full_analysis_deep_link: "/test/run-reviews?issue=issue-parent",
      },
      root: {
        issue: {
          id: "issue-parent",
          workspace_id: "workspace-1",
          number: 1,
          identifier: "GTD-1",
          title: "user-center 父任务",
          description: null,
          status: "in_progress",
          priority: "medium",
          assignee_type: "squad",
          assignee_id: "squad-1",
          creator_type: "member",
          creator_id: "user-1",
          parent_issue_id: null,
          project_id: "project-usercenter",
          position: 1,
          start_date: null,
          due_date: null,
          created_at: "2026-06-08T08:00:00Z",
          updated_at: "2026-06-08T08:00:00Z",
          metadata: {},
        },
        tasks: [makeTask({ id: "task-parent", status: "completed", issue_id: "issue-parent" })],
        sop_runs: [{ id: "run-1", events: [{ id: "event-1" }, { id: "event-2" }] }],
        trace_events: [{ id: "trace-1" }, { id: "trace-2" }],
        tool_call_chains: [
          {
            id: "tool:call-1",
            task_id: "task-parent",
            tool: "curl-check",
            status: "已配对",
            use_seq: 1,
            result_seq: 2,
            input: { url: "/health" },
            output: "Error: HTTP 500 from upstream",
            duration_ms: 1200,
            result_category: "异常线索",
            failure_signal: true,
            failure_reason: "工具结果包含 HTTP 状态码 500",
            summary: "工具 curl-check 已配对：调用 #1，结果 #2",
            created_at: "2026-06-08T08:02:00Z",
            completed_at: "2026-06-08T08:02:01Z",
          },
        ],
        tool_call_summary: [
          {
            tool: "curl-check",
            total_calls: 2,
            paired_calls: 2,
            missing_result_calls: 0,
            orphan_result_calls: 0,
            average_duration_ms: 900,
            max_duration_ms: 1200,
            slowest_tool_call_chain_id: "tool:call-1",
            result_categories: { 已返回: 1, 异常线索: 1 },
            failure_signal_calls: 1,
            needs_attention: true,
            summary: "curl-check：调用 2 次，异常线索 1 次",
          },
        ],
        wakeup_comments: [
          {
            id: "comment-1",
            issue_id: "issue-parent",
            author_type: "system",
            type: "system",
            content: "子任务 [GTD-2]「gateway 子任务」已完成。",
            parent_id: null,
            created_at: "2026-06-08T08:03:00Z",
          },
        ],
        children: [
          {
            issue: {
              id: "issue-child",
              workspace_id: "workspace-1",
              number: 2,
              identifier: "GTD-2",
              title: "gateway 子任务",
              description: null,
              status: "done",
              priority: "medium",
              assignee_type: null,
              assignee_id: null,
              creator_type: "member",
              creator_id: "user-1",
              parent_issue_id: "issue-parent",
              project_id: "project-gateway",
              position: 2,
              start_date: null,
              due_date: null,
              created_at: "2026-06-08T08:01:00Z",
              updated_at: "2026-06-08T08:02:00Z",
              metadata: {},
            },
            tasks: [makeTask({ id: "task-child", status: "queued", issue_id: "issue-child" })],
            sop_runs: [],
            trace_events: [{ id: "trace-child" }],
            tool_call_chains: [],
            tool_call_summary: [],
            wakeup_comments: [],
            children: [],
          },
        ],
      },
    });

    renderWithQuery(<IssueRunReviewSummaryCard issueId="issue-parent" />);

    expect(await screen.findByText("运行复盘")).toBeInTheDocument();
    expect(screen.getByText("验收：failed")).toBeInTheDocument();
    expect(screen.getByText("总耗时：1m 5s")).toBeInTheDocument();
    expect(screen.getByText("任务数：2")).toBeInTheDocument();
    expect(screen.getByText("异常数：1")).toBeInTheDocument();
    expect(screen.getByText("Token：170")).toBeInTheDocument();
    expect(screen.getByText("验收失败：缺少执行级结论")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "查看完整复盘" })).toHaveAttribute(
      "href",
      "/test/run-reviews?issue=issue-parent",
    );
    expect(screen.queryByText("协作执行树")).not.toBeInTheDocument();
    expect(screen.queryByText("Issue 运行时间流")).not.toBeInTheDocument();
    expect(screen.queryByText("工具链明细")).not.toBeInTheDocument();
    expect(screen.queryByText(/父任务 GTD-1/)).not.toBeInTheDocument();
    expect(screen.queryByText(/子任务 GTD-2/)).not.toBeInTheDocument();
  });
});
