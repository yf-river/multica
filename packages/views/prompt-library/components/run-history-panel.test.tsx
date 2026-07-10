// @vitest-environment jsdom

import { fireEvent, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import type { PromptEvaluationRun } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { RunHistoryPanel } from "./run-history-panel";

const run = {
  id: "run-1",
  workspace_id: "workspace-1",
  asset_id: "asset-1",
  prompt_id: "prompt-1",
  run_kind: "Agent执行",
  status: "需人工复核",
  trigger_source: "manual",
  agent_id: "agent-1",
  runtime_id: "runtime-1",
  task_id: "task-1",
  chat_session_id: null,
  model: "gpt-5.6",
  runtime_provider: "codex",
  total_cases: 2,
  passed_cases: 1,
  failed_cases: 1,
  pass_rate: 0.5,
  total_duration_ms: 1250,
  average_duration_ms: 625,
  input_tokens: 10,
  output_tokens: 20,
  estimated_cost: 0.01,
  failure_reason: "缺少结论",
  conclusion: "需要复核",
  metrics: {},
  evidence: {},
  started_at: "2026-07-10T00:00:00Z",
  completed_at: "2026-07-10T00:00:01Z",
  created_by: null,
  created_at: "2026-07-10T00:00:00Z",
  updated_at: "2026-07-10T00:00:01Z",
  review_decision: "",
  review_note: "",
  reviewed_by: null,
  reviewed_at: "",
} satisfies PromptEvaluationRun;

function renderPanel(overrides: Partial<React.ComponentProps<typeof RunHistoryPanel>> = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const props: React.ComponentProps<typeof RunHistoryPanel> = {
    workspaceId: "workspace-1",
    runs: [run],
    focusedRunId: null,
    evidenceFocus: {
      traceSeq: null,
      toolChainId: null,
      trialAnchor: null,
      assertionAnchor: null,
      messageSeq: null,
      spanAnchor: null,
      failureAnchor: null,
    },
    runStatusFilter: "全部",
    onRunStatusFilterChange: vi.fn(),
    candidates: [],
    skillResources: [],
    loading: false,
    onSyncRun: vi.fn(),
    syncingRunId: null,
    onCancelRun: vi.fn(),
    cancellingRunId: null,
    onReviewRun: vi.fn(),
    reviewingRunId: null,
    onCreateEvidenceSnapshot: vi.fn(),
    creatingEvidenceSnapshotRunId: null,
    onGenerateCandidate: vi.fn(),
    generatingCandidateRunId: null,
    ...overrides,
  };
  renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <RunHistoryPanel {...props} />
    </QueryClientProvider>,
  );
  return props;
}

describe("RunHistoryPanel", () => {
  it("renders current run actions and returns protocol decisions", () => {
    const props = renderPanel();

    expect(screen.getByText("智能体执行 · 需人工复核")).toBeInTheDocument();
    expect(screen.getByText(/模型 gpt-5.6 · 运行时 codex/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "同步任务" }));
    expect(props.onSyncRun).toHaveBeenCalledWith("run-1");
    fireEvent.click(screen.getByRole("button", { name: "人工通过" }));
    expect(props.onReviewRun).toHaveBeenCalledWith(run, "通过");
  });

  it("maps the review queue label back to its persisted status", () => {
    const onRunStatusFilterChange = vi.fn();
    renderPanel({ onRunStatusFilterChange });

    fireEvent.click(screen.getByRole("button", { name: "人工复核队列" }));
    expect(onRunStatusFilterChange).toHaveBeenCalledWith("需人工复核");
  });
});
