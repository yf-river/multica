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
  cancelTask: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listTasksByIssue: mockState.listTasksByIssue,
    listIssueTaskTraceEvents: mockState.listIssueTaskTraceEvents,
    cancelTask: mockState.cancelTask,
  },
}));

vi.mock("@multica/core/chat/queries", () => ({
  taskMessagesOptions: mockState.taskMessagesOptions,
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

import { ActiveTaskRow, ExecutionLogSection } from "./execution-log-section";

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
  it("renders durable trace evidence with Chinese labels", async () => {
    vi.useRealTimers();
    mockState.listTasksByIssue.mockResolvedValue([makeTask()]);
    mockState.listIssueTaskTraceEvents.mockResolvedValue({
      events: [
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

    renderWithQuery(<ExecutionLogSection issueId="issue-1" />);

    expect(await screen.findByText("观测事件")).toBeInTheDocument();
    expect(screen.getByText("任务事件树")).toBeInTheDocument();
    expect(screen.getByText("根任务 task-1")).toBeInTheDocument();
    expect(screen.getByText("用量事件 1")).toBeInTheDocument();
    expect(screen.getByText("模型用量")).toBeInTheDocument();
    expect(screen.getByText("模型用量已上报")).toBeInTheDocument();
    expect(screen.getAllByText(/150 tokens/).length).toBeGreaterThan(0);
  });
});
