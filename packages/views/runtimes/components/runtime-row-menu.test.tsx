// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { AgentRuntime } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enRuntimes from "../../locales/zh-Hans/runtimes.json";
import enAgents from "../../locales/zh-Hans/agents.json";

const TEST_RESOURCES = {
  "zh-Hans": { common: enCommon, runtimes: enRuntimes, agents: enAgents },
};

const permissionState = vi.hoisted(() => ({
  userId: "user-me",
  role: "owner",
}));

// RuntimeList reads workspace caches for workload, owner, and cost columns.
// This contract keeps those unrelated rows empty while exercising the real
// list composition around CLI and action cells.
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

vi.mock("@multica/core/runtimes/mutations", () => ({
  useDeleteRuntime: () => ({ mutate: vi.fn(), isPending: false, mutateAsync: vi.fn() }),
  useArchiveAgentsAndDeleteRuntime: () => ({
    mutate: vi.fn(),
    isPending: false,
    mutateAsync: vi.fn(),
  }),
}));

vi.mock("@multica/core/runtimes", () => ({
  deriveRuntimeHealth: () => "online",
  runtimeUsageOptions: () => ({ kind: "usage" }),
}));

vi.mock("@multica/core/agents", () => ({
  deriveWorkload: () => "idle",
  agentTaskSnapshotOptions: () => ({ queryKey: ["agent-task-snapshot"] }),
  agentTaskWorkloadKind: () => null,
  useWorkspacePresenceMap: () => ({ byAgent: new Map(), loading: false }),
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => permissionState,
  canManageWorkspace: (role: string) => role === "owner" || role === "admin",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspaceId: () => "ws-1",
  useWorkspacePaths: () => ({
    runtimeDetail: (runtimeId: string) => `/acme/runtimes/${runtimeId}`,
  }),
}));

vi.mock("../../navigation", () => ({
  useRowLink: () => (href: string) => ({ href }),
}));

// The unified DeleteRuntimeDialog the kebab now opens reaches into auth +
// the api singleton. The dialog never renders in these tests (`open=false`
// throughout) but its hooks still mount; stub them so module init is clean.
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (sel: (s: { user: { id: string } }) => unknown) =>
    sel({ user: { id: "user-me" } }),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    deleteRuntime: vi.fn(),
    archiveAgentsAndDeleteRuntime: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("../../common/use-viewing-timezone", () => ({
  useViewingTimezone: () => "UTC",
}));

vi.mock("./provider-logo", () => ({ ProviderLogo: () => null }));
vi.mock("./shared", () => ({
  HealthIcon: () => null,
  RuntimeVisibilityBadge: () => null,
  useHealthLabel: () => () => "Online",
}));

import { RuntimeList } from "./runtime-list";

function makeRuntime(overrides: Partial<AgentRuntime>): AgentRuntime {
  return {
    id: "rt-1",
    daemon_id: null,
    name: "rt",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: "user-1",
    scope: "personal",
    profile_id: null,
    last_seen_at: null,
    ...overrides,
  };
}

function renderRuntimeList(runtime: AgentRuntime) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  return render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <QueryClientProvider client={qc}>
        <RuntimeList runtimes={[runtime]} now={Date.now()} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

describe("RuntimeList", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    permissionState.userId = "user-me";
    permissionState.role = "owner";
  });

  it.each([
    ["online local", { runtime_mode: "local", status: "online" }],
    ["offline local", { runtime_mode: "local", status: "offline" }],
    ["online cloud", { runtime_mode: "cloud", status: "online" }],
  ] as const)(
    "renders the kebab menu for an editable %s runtime",
    (_case, fields) => {
      renderRuntimeList(makeRuntime(fields));
      expect(screen.getByLabelText("行操作")).toBeInTheDocument();
    },
  );

  it("hides the kebab menu when the caller lacks delete permission", () => {
    permissionState.role = "member";
    renderRuntimeList(
      makeRuntime({
        runtime_mode: "local",
        status: "offline",
        owner_id: "another-user",
      }),
    );
    expect(screen.queryByLabelText("行操作")).not.toBeInTheDocument();
  });

  it("shows the agent's own CLI tool version, not the shared daemon version", () => {
    renderRuntimeList(
      makeRuntime({
        runtime_mode: "local",
        metadata: { version: "2.1.5 (Claude Code)", cli_version: "0.3.17" },
      }),
    );
    expect(screen.getByText("2.1.5 (Claude Code)")).toBeInTheDocument();
    expect(screen.queryByText("0.3.17")).not.toBeInTheDocument();
  });

  it("falls back to an em dash when the agent version is missing", () => {
    renderRuntimeList(
      makeRuntime({
        runtime_mode: "local",
        metadata: { cli_version: "0.3.17" },
      }),
    );
    expect(screen.queryByText("0.3.17")).not.toBeInTheDocument();
    expect(screen.getAllByText("—")).toHaveLength(3);
  });
});
