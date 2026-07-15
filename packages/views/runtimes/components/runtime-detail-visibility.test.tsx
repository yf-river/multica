// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import type { AgentRuntime } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enRuntimes from "../../locales/zh-Hans/runtimes.json";
import enAgents from "../../locales/zh-Hans/agents.json";

const TEST_RESOURCES = {
  "zh-Hans": { common: enCommon, runtimes: enRuntimes, agents: enAgents },
};

const mockUpdateRuntime = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    updateRuntime: (...args: unknown[]) => mockUpdateRuntime(...args),
    deleteRuntime: vi.fn(),
    archiveAgentsAndDeleteRuntime: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>(
      "@tanstack/react-query",
    );
  return {
    ...actual,
    useQuery: vi.fn(() => ({ data: [], isLoading: false })),
  };
});

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (sel: (s: { user: { id: string } }) => unknown) =>
    sel({ user: { id: "user-me" } }),
}));

vi.mock("@multica/core/runtimes", () => ({
  deriveRuntimeHealth: () => "online",
  useRuntimeNow: () => Date.parse("2026-04-27T12:00:00Z"),
}));

vi.mock("@multica/core/agents", () => ({
  useWorkspacePresenceMap: () => ({ byAgent: new Map() }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspaceId: () => "ws-1",
  useWorkspacePaths: () => ({
    runtimes: () => "/runtimes",
    agentDetail: () => "/agents",
  }),
}));

vi.mock("@multica/core/runtimes/mutations", () => ({
  useUpdateRuntime: () => ({
    mutate: (
      args: { runtimeId: string; scope: "personal" | "workspace" },
      opts?: { onSuccess?: () => void; onError?: () => void },
    ) => {
      mockUpdateRuntime(args.runtimeId, { scope: args.scope });
      opts?.onSuccess?.();
    },
    isPending: false,
  }),
  useDeleteRuntime: () => ({ mutate: vi.fn(), isPending: false, mutateAsync: vi.fn() }),
  useArchiveAgentsAndDeleteRuntime: () => ({
    mutate: vi.fn(),
    isPending: false,
    mutateAsync: vi.fn(),
  }),
}));

// Stubbing ProviderLogo / UsageSection avoids dragging in chart libs and
// additional query keys we don't care about here.
vi.mock("./provider-logo", () => ({ ProviderLogo: () => null }));
vi.mock("./usage-section", () => ({ UsageSection: () => null }));
vi.mock("./shared", () => ({
  HealthBadge: () => null,
  RuntimeVisibilityBadge: ({ runtime }: { runtime: AgentRuntime }) => (
    <span>{runtime.scope === "workspace" ? "工作区" : "个人"}</span>
  ),
}));
vi.mock("../../agents/presence", () => ({
  availabilityConfig: { offline: { dotClass: "", textClass: "" } },
  workloadConfig: { idle: { icon: () => null, textClass: "" } },
}));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("../../navigation", () => ({
  AppLink: () => null,
  useNavigation: () => ({ push: vi.fn(), replace: vi.fn() }),
}));

import { RuntimeDetail } from "./runtime-detail";

function makeRuntime(overrides: Partial<AgentRuntime>): AgentRuntime {
  return {
    id: "rt-1",
    daemon_id: null,
    name: "Local Runtime",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "host.local",
    metadata: {},
    owner_id: "user-me",
    scope: "personal",
    profile_id: null,
    last_seen_at: "2026-04-27T11:59:50Z",
    ...overrides,
  };
}

function renderDetail(runtime: AgentRuntime) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <QueryClientProvider client={qc}>
        <RuntimeDetail runtime={runtime} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

describe("RuntimeDetail visibility section", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows owner-editable visibility choices when the caller owns the runtime", () => {
    renderDetail(makeRuntime({ owner_id: "user-me" }));
    expect(screen.getByText("使用范围")).toBeInTheDocument();
    expect(screen.getByText("个人")).toBeInTheDocument();
    expect(screen.getByText("工作区")).toBeInTheDocument();
  });

  it("flips scope to workspace when the owner clicks the workspace choice", async () => {
    renderDetail(makeRuntime({ owner_id: "user-me", scope: "personal" }));
    fireEvent.click(screen.getByText("工作区"));
    await waitFor(() =>
      expect(mockUpdateRuntime).toHaveBeenCalledWith("rt-1", { scope: "workspace" }),
    );
  });

  it("renders a read-only visibility chip when the caller cannot edit", () => {
    renderDetail(makeRuntime({ owner_id: "someone-else", scope: "workspace" }));
    expect(screen.getByText("工作区")).toBeInTheDocument();
    // The editor's "个人" choice button must not render in read-only mode.
    expect(screen.queryByText("个人")).not.toBeInTheDocument();
  });

  it("renders an enabled Delete runtime button for an owner on a self-healing local runtime", () => {
    renderDetail(
      makeRuntime({
        owner_id: "user-me",
        runtime_mode: "local",
        status: "online",
      }),
    );
    const btn = screen.getByRole("button", {
      name: /删除运行时/i,
    }) as HTMLButtonElement;
    expect(btn.disabled).toBe(false);
  });

  it("hides the Delete runtime button entirely for callers who cannot edit", () => {
    renderDetail(
      makeRuntime({
        owner_id: "someone-else",
        runtime_mode: "local",
        status: "online",
      }),
    );
    expect(
      screen.queryByRole("button", { name: /删除运行时/i }),
    ).not.toBeInTheDocument();
  });
});
