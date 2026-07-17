/**
 * @vitest-environment jsdom
 */
import { createElement, type ReactNode } from "react";
import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Agent, AgentTask } from "@multica/core/types";
import { renderWithI18n } from "../../../test/i18n";
import { ActivityTab } from "./activity-tab";

const NOW = new Date("2026-04-28T12:00:00Z").getTime();
const HOUR = 60 * 60 * 1000;
const DAY = 24 * HOUR;
const queryState = vi.hoisted(() => ({ tasks: [] as AgentTask[] }));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>(
    "@tanstack/react-query",
  );
  return {
    ...actual,
    useQuery: vi.fn((options: { queryKey?: readonly unknown[] }) => ({
      data: options.queryKey?.[0] === "agent-tasks" ? queryState.tasks : [],
      isLoading: false,
    })),
    useQueries: vi.fn(() => []),
  };
});

vi.mock("@multica/core/agents", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/agents")>(
    "@multica/core/agents",
  );
  return {
    ...actual,
    agentTaskSnapshotOptions: () => ({ queryKey: ["agent-snapshot"] }),
    agentTasksOptions: () => ({ queryKey: ["agent-tasks"] }),
    useWorkspaceActivityMap: () => ({
      byAgent: new Map([
        [
          "agent-1",
          {
            buckets: Array.from({ length: 30 }, (_, index) => ({
              total: index === 29 ? 1 : 0,
              failed: 0,
            })),
          },
        ],
      ]),
      loading: false,
    }),
  };
});

vi.mock("@multica/core/paths", () => ({
  useWorkspaceId: () => "ws-1",
  useWorkspacePaths: () => ({
    issueDetail: (issueId: string) => `/acme/issues/${issueId}`,
  }),
}));

vi.mock("../../../navigation", () => ({
  AppLink: ({ children }: { children: ReactNode }) => children,
}));

vi.mock("../../../common/task-transcript", () => ({
  TranscriptButton: () => null,
}));

const agent = {
  id: "agent-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "cloud",
  runtime_config: {},
  custom_args: [],
  custom_env_key_count: 0,
  mcp_config: null,
  mcp_config_redacted: false,
  scope: "workspace",
  max_concurrent_tasks: 1,
  model: "",
  thinking_level: "",
  owner_id: null,
  skills: [],
  created_at: new Date(NOW - 100 * DAY).toISOString(),
  updated_at: new Date(NOW).toISOString(),
  archived_at: null,
} as Agent;

function task(startedAt: number, completedAt: number): AgentTask {
  return {
    id: `task-${startedAt}`,
    agent_id: agent.id,
    runtime_id: "runtime-1",
    issue_id: "",
    status: "completed",
    dispatched_at: null,
    started_at: new Date(startedAt).toISOString(),
    completed_at: new Date(completedAt).toISOString(),
    result: null,
    error: null,
    created_at: new Date(startedAt).toISOString(),
  };
}

afterEach(() => {
  vi.useRealTimers();
  queryState.tasks = [];
});

describe("ActivityTab", () => {
  it.each([
    [800, "平均 1s"],
    [125_000, "平均 2m 05s"],
    [3 * HOUR + 30 * 60_000, "平均 3h 30m"],
  ] as const)("renders the current average duration as %s ms", (duration, label) => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
    queryState.tasks = [task(NOW - duration, NOW)];

    renderWithI18n(createElement(ActivityTab, { agent }));

    expect(screen.getByText(label)).toBeInTheDocument();
  });

  it("excludes old and non-positive task durations from the visible average", () => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
    queryState.tasks = [
      task(NOW - 60_000, NOW),
      task(NOW - 60 * DAY - 180_000, NOW - 60 * DAY),
      task(NOW, NOW - 1000),
    ];

    renderWithI18n(createElement(ActivityTab, { agent }));

    expect(screen.getByText("平均 1m 00s")).toBeInTheDocument();
  });
});
