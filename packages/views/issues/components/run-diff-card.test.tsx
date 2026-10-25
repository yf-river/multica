// @vitest-environment jsdom

import { cleanup, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AgentTask } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const listTasksByIssue = vi.fn();

vi.mock("@multica/core/api", () => ({
  api: { getIssueTaskRun: (...args: unknown[]) => listTasksByIssue(...args).then((tasks:AgentTask[])=>tasks.find(task=>task.id===args[1])??null) },
}));

import { RunDiffCard } from "./run-diff-card";

const codeDiff = {
  before: "123456789012345",
  after: "987654321098765",
  truncated: false,
  unavailable: false,
  files: [
    { path: "src/notes.ts", added: 1, deleted: 0, binary: false, truncated: false, patch: "@@ -0,0 +1 @@\n+第五条" },
  ],
};

function makeTask(overrides: Partial<AgentTask> = {}): AgentTask {
  return {
    id: "task-1",
    agent_id: "agent-1",
    runtime_id: "runtime-1",
    issue_id: "issue-1",
    status: "completed",
    priority: 0,
    dispatched_at: null,
    started_at: "2026-10-22T01:00:00Z",
    completed_at: "2026-10-22T01:02:00Z",
    result: { code_diff: codeDiff },
    error: null,
    created_at: "2026-10-22T01:00:00Z",
    trigger_summary: "Started from comment",
    ...overrides,
  };
}

function renderCard(tasks: AgentTask[] | Error, taskId = "task-1") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  if (tasks instanceof Error) {
    listTasksByIssue.mockRejectedValue(tasks);
  } else {
    client.setQueryData(["run-diff","issue-1",taskId,"summary"], tasks.find(task=>task.id===taskId)??null);
    listTasksByIssue.mockResolvedValue(tasks);
  }
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <RunDiffCard issueId="issue-1" taskId={taskId} />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("RunDiffCard", () => {
  it("shows the run's snapshot for a finished run with changes", async () => {
    renderCard([makeTask()]);
    expect(await screen.findByRole("button", { name: /查看文件修改：notes.ts/ })).toBeTruthy();
  });

  it("renders nothing while the run has no diff yet", () => {
    const { container } = renderCard([makeTask({ status: "running", result: null })]);
    expect(container.textContent).toBe("");
  });

  it("renders nothing when the run changed no files", () => {
    const { container } = renderCard([makeTask({ result: { code_diff: { ...codeDiff, files: [] } } })]);
    expect(container.textContent).toBe("");
  });

  it("renders nothing for a task outside this issue's runs", () => {
    const { container } = renderCard([makeTask({ id: "task-other" })], "task-1");
    expect(container.textContent).toBe("");
  });
});
