/**
 * @vitest-environment jsdom
 */
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Agent, AgentActivityBucket } from "../types";
import { workspaceKeys } from "../workspace/queries";
import { agentActivityKeys } from "./queries";
import {
  type AgentActivity,
  summarizeActivityWindow,
  useWorkspaceActivityMap,
} from "./use-agent-activity";

const DAY = 24 * 60 * 60 * 1000;
const NOW = new Date("2026-04-28T12:00:00").getTime();

function bucket(
  agentId: string,
  daysAgo: number,
  taskCount: number,
  failedCount = 0,
): AgentActivityBucket {
  const t = new Date(NOW);
  t.setHours(0, 0, 0, 0);
  return {
    agent_id: agentId,
    bucket_at: new Date(t.getTime() - daysAgo * DAY).toISOString(),
    task_count: taskCount,
    failed_count: failedCount,
  };
}

const agent: Agent = {
  id: "a1",
  runtime_id: "r1",
  name: "Old Agent",
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
};

function renderActivityMap(
  agents: Agent[],
  buckets: AgentActivityBucket[],
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  queryClient.setQueryData(workspaceKeys.agents("ws-1"), agents);
  queryClient.setQueryData(agentActivityKeys.last30d("ws-1"), buckets);
  function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client: queryClient }, children);
  }
  return renderHook(() => useWorkspaceActivityMap("ws-1"), {
    wrapper: Wrapper,
  }).result.current.byAgent;
}

function activityWith(...entries: Array<[daysAgo: number, total: number, failed?: number]>): AgentActivity {
  const buckets = Array.from({ length: 30 }, () => ({ total: 0, failed: 0 }));
  for (const [daysAgo, total, failed = 0] of entries) {
    buckets[29 - daysAgo] = { total, failed };
  }
  return { buckets };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("useWorkspaceActivityMap", () => {
  it("groups current workspace buckets into zero-filled 30-day agent series", () => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
    const map = renderActivityMap(
      [agent, { ...agent, id: "a2" }, { ...agent, id: "a3" }],
      [
        bucket("a1", 29, 1),
        bucket("a1", 0, 5),
        bucket("a2", 1, 2, 1),
        bucket("a2", 60, 99),
      ],
    );

    expect(map.size).toBe(3);
    expect(map.get("a1")?.buckets).toHaveLength(30);
    expect(map.get("a1")?.buckets[0]).toEqual({ total: 1, failed: 0 });
    expect(map.get("a1")?.buckets[29]).toEqual({ total: 5, failed: 0 });
    expect(summarizeActivityWindow(map.get("a2"), 30)).toMatchObject({
      totalRuns: 2,
      totalFailed: 1,
    });
    expect(map.get("a3")?.buckets.every((value) => value.total === 0 && value.failed === 0)).toBe(true);
  });
});

describe("summarizeActivityWindow", () => {
  it("rolls up the requested trailing window and clamps it to the series", () => {
    const activity = activityWith([25, 1], [6, 1], [0, 3, 1]);

    expect(summarizeActivityWindow(activity, 7)).toMatchObject({
      totalRuns: 4,
      totalFailed: 1,
    });
    expect(summarizeActivityWindow(activity, 7).buckets).toHaveLength(7);
    expect(summarizeActivityWindow(activity, 1000)).toMatchObject({
      totalRuns: 5,
      totalFailed: 1,
    });
    expect(summarizeActivityWindow(activity, 1000).buckets).toHaveLength(30);
  });

  it("returns an empty summary for missing activity or a zero-day window", () => {
    expect(summarizeActivityWindow(undefined, 7)).toEqual({
      buckets: [],
      totalRuns: 0,
      totalFailed: 0,
    });
    expect(summarizeActivityWindow(activityWith([0, 5]), 0)).toEqual({
      buckets: [],
      totalRuns: 0,
      totalFailed: 0,
    });
  });
});
