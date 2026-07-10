// @vitest-environment jsdom

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type {
  Agent,
  AgentPlaygroundDetail,
  PromptEvaluationAsset,
  PromptEvaluationDatasetVersion,
} from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mockApi = vi.hoisted(() => ({
  listExperiments: vi.fn(),
  getExperiment: vi.fn(),
  listAgents: vi.fn(),
  listAssets: vi.fn(),
  listVersions: vi.fn(),
  createExperiment: vi.fn(),
  runExperiment: vi.fn(),
  syncExperiment: vi.fn(),
  judgeExperiment: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listAgentPlaygroundExperiments: mockApi.listExperiments,
    getAgentPlaygroundExperiment: mockApi.getExperiment,
    listAgents: mockApi.listAgents,
    listPromptEvaluationAssets: mockApi.listAssets,
    listPromptEvaluationDatasetVersions: mockApi.listVersions,
    createAgentPlaygroundExperiment: mockApi.createExperiment,
    runAgentPlaygroundExperiment: mockApi.runExperiment,
    syncAgentPlaygroundExperiment: mockApi.syncExperiment,
    judgeAgentPlaygroundExperiment: mockApi.judgeExperiment,
  },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ agents: () => "/workspace/agents" }),
}));

vi.mock("@multica/core/realtime", () => ({
  useWSEvent: vi.fn(),
  useWSReconnect: vi.fn(),
}));

import { AgentPlaygroundPage } from "./agent-playground-page";

const agent = {
  id: "agent-1",
  name: "Codex Agent",
} as Agent;

const dataset = {
  id: "dataset-1",
  name: "登录回归集",
} as PromptEvaluationAsset;

const version = {
  id: "version-1",
  dataset_asset_id: dataset.id,
  version: 1,
  version_label: "稳定快照",
  row_count: 3,
} as PromptEvaluationDatasetVersion;

const createdDetail = {
  experiment: {
    id: "experiment-1",
    workspace_id: "workspace-1",
    name: "Agent 对比实验",
    description: "",
    dataset_asset_id: dataset.id,
    dataset_version_id: version.id,
    judge_agent_id: null,
    status: "draft",
    created_by: "user-1",
    created_at: "2026-07-10T00:00:00Z",
    updated_at: "2026-07-10T00:00:00Z",
    input_count: 3,
    agent_count: 1,
  },
  inputs: [],
  agents: [],
  results: [],
  judgements: [],
} satisfies AgentPlaygroundDetail;

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <AgentPlaygroundPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockApi.listExperiments.mockResolvedValue({ items: [], total: 0 });
  mockApi.getExperiment.mockResolvedValue(createdDetail);
  mockApi.listAgents.mockResolvedValue([]);
  mockApi.listAssets.mockResolvedValue({ items: [], total: 0 });
  mockApi.listVersions.mockResolvedValue({ items: [], total: 0 });
  mockApi.createExperiment.mockResolvedValue(createdDetail);
});

describe("AgentPlaygroundPage", () => {
  it("shows the current creation requirements and empty experiment state", async () => {
    renderPage();

    expect(screen.getByText("Agent 调试场")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "创建实验" })).toBeDisabled();
    expect(await screen.findByText("还需要：选择用例库、选择执行 Agent")).toBeInTheDocument();
    expect(screen.getByText("暂无实验。创建一个实验后会在这里看到结果矩阵。")).toBeInTheDocument();
  });

  it("creates an experiment from one dataset snapshot and selected agent", async () => {
    mockApi.listAgents.mockResolvedValue([agent]);
    mockApi.listAssets.mockResolvedValue({ items: [dataset], total: 1 });
    mockApi.listVersions.mockResolvedValue({ items: [version], total: 1 });
    renderPage();

    await screen.findByRole("option", { name: dataset.name });
    fireEvent.change(screen.getByLabelText("用例库"), {
      target: { value: dataset.id },
    });
    await waitFor(() => {
      expect(mockApi.listVersions).toHaveBeenCalledWith(dataset.id, 20);
    });
    await screen.findByRole("option", { name: /v1 稳定快照 · 3 条/ });

    fireEvent.click(screen.getByRole("button", { name: "搜索并选择执行 Agent" }));
    fireEvent.click(await screen.findByRole("button", { name: agent.name }));

    const createButton = screen.getByRole("button", { name: "创建实验" });
    await waitFor(() => expect(createButton).toBeEnabled());
    expect(screen.getByText("本次将创建 3 条用例 × 1 个 Agent = 3 个执行任务。")).toBeInTheDocument();
    fireEvent.click(createButton);

    await waitFor(() => {
      expect(mockApi.createExperiment).toHaveBeenCalledWith({
        name: "Agent 对比实验",
        description: "",
        dataset_asset_id: dataset.id,
        dataset_version_id: version.id,
        judge_agent_id: undefined,
        agent_ids: [agent.id],
      });
    });
  });
});
