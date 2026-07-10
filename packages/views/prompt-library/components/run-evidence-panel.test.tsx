// @vitest-environment jsdom

import { screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { PromptEvaluationRunEvidence } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mockApi = vi.hoisted(() => ({
  listPromptEvaluationOptimizationCandidates: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: mockApi,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

import { RunEvidencePanel } from "./run-evidence-panel";

const evidence = {
  run: {
    id: "run-1",
    status: "未通过",
    failed_cases: 1,
    input_tokens: 10,
    output_tokens: 5,
    task_id: "task-1",
    model: "gpt-5.6",
    runtime_provider: "codex",
    passed_cases: 0,
    total_cases: 1,
    total_duration_ms: 1500,
    estimated_cost: 0.001,
  },
  trials: [],
  task_usage: [],
  task_messages: [],
  trace_events: [],
  execution_spans: [],
  tool_call_chains: [],
  tool_call_summary: [],
  execution_summary: {},
  evidence: { raw: "保留原始证据" },
  上下文: { dispatch: "mention://agent/agent-1" },
} as unknown as PromptEvaluationRunEvidence;

beforeEach(() => {
  vi.clearAllMocks();
  mockApi.listPromptEvaluationOptimizationCandidates.mockResolvedValue({ items: [], total: 0 });
});

describe("RunEvidencePanel", () => {
  it("opens raw evidence for every supported deep-link focus and preserves dispatch mentions", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderWithI18n(
      <QueryClientProvider client={queryClient}>
        <RunEvidencePanel
          evidence={evidence}
          snapshots={[]}
          snapshotsLoading={false}
          loading={false}
          error={false}
          skillResources={[]}
          evidenceFocus={{
            traceSeq: null,
            toolChainId: null,
            trialAnchor: "trial-1",
            assertionAnchor: null,
            messageSeq: null,
            spanAnchor: null,
            failureAnchor: null,
          }}
          creatingSnapshot={false}
          onCreateSnapshot={vi.fn()}
        />
      </QueryClientProvider>,
    );

    expect(screen.getByText("trial trial-1")).toBeInTheDocument();
    expect(screen.getByText("完整原始 evidence JSON").closest("details")).toHaveAttribute("open");
    expect(screen.getByText(/mention:\/\/agent\/agent-1/)).toBeInTheDocument();
  });
});
