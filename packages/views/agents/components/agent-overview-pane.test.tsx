// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enAgents from "../../locales/zh-Hans/agents.json";

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, agents: enAgents } };

// AgentOverviewPane pulls in ActorIssuesPanel which in turn touches the api
// layer. The test only cares about which top-of-pane tab buttons render,
// not what each tab does, so we stub the heavy children.
vi.mock("./tabs/activity-tab", () => ({
  ActivityTab: () => <div>activity-tab</div>,
}));
vi.mock("./tabs/instructions-tab", () => ({
  InstructionsTab: ({ onDirtyChange }: { onDirtyChange?: (dirty: boolean) => void }) => (
    <button type="button" onClick={() => onDirtyChange?.(true)}>
      mark-instructions-dirty
    </button>
  ),
}));
vi.mock("./tabs/skills-tab", () => ({
  SkillsTab: () => <div>skills-tab</div>,
}));
vi.mock("./tabs/env-tab", () => ({
  EnvTab: () => <div>env-tab</div>,
}));
vi.mock("./tabs/custom-args-tab", () => ({
  CustomArgsTab: () => <div>custom-args-tab</div>,
}));
vi.mock("./tabs/mcp-config-tab", () => ({
  McpConfigTab: () => <div>mcp-config-tab</div>,
}));
vi.mock("./tabs/integrations-tab", () => ({
  IntegrationsTab: () => <div>integrations-tab</div>,
}));
vi.mock("../../common/actor-issues-panel", () => ({
  ActorIssuesPanel: () => <div>actor-issues-panel</div>,
}));

// The pane now reads workspace context to decide whether the 集成
// tab is worth showing (it queries Lark installations to learn whether the
// deployment has the feature configured). Provide a stable workspace id and
// a listing query backed by a ref so each test can flip `configured`.
const larkListingRef = vi.hoisted(() => ({
  current: { installations: [] as unknown[], configured: false },
}));
vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/lark", () => ({
  larkInstallationsOptions: () => ({
    queryKey: ["lark", "installations"],
    queryFn: () => Promise.resolve(larkListingRef.current),
  }),
}));

import { AgentOverviewPane, type DetailTab } from "./agent-overview-pane";

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  scope: "workspace",
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function makeRuntime(provider: string): AgentRuntime {
  return {
    id: "runtime-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Runtime",
    runtime_mode: "local",
    provider,
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: null,
    scope: "personal",
    last_seen_at: null,
    created_at: "2026-05-28T00:00:00Z",
    updated_at: "2026-05-28T00:00:00Z",
  };
}

interface NavigationProps {
  navIntent?: DetailTab | null;
  onNavIntentHandled?: () => void;
}

function renderPane(runtimes: AgentRuntime[], navigationProps: NavigationProps = {}) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const renderTree = (props: NavigationProps) => (
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <AgentOverviewPane
          agent={baseAgent}
          runtimes={runtimes}
          onUpdate={vi.fn().mockResolvedValue(undefined)}
          {...props}
        />
      </QueryClientProvider>
    </I18nProvider>
  );
  const result = render(renderTree(navigationProps));
  return {
    ...result,
    rerenderPane: (props: NavigationProps) => result.rerender(renderTree(props)),
  };
}

beforeEach(() => {
  larkListingRef.current = { installations: [], configured: false };
});

describe("AgentOverviewPane MCP tab visibility", () => {
  it.each([
    ["Claude", "claude"],
    ["Codex", "codex"],
    ["Cursor", "cursor"],
    ["Hermes", "hermes"],
    ["Kimi", "kimi"],
    ["Kiro", "kiro"],
    ["OpenCode", "opencode"],
    ["OpenClaw", "openclaw"],
  ])("renders the MCP tab when the agent runs on the %s runtime", (_label, provider) => {
    renderPane([makeRuntime(provider)]);
    expect(screen.getByRole("button", { name: /^MCP$/i })).toBeInTheDocument();
  });

  it("hides the MCP tab for providers whose backend does not read mcp_config", () => {
    // Saving an MCP config on e.g. Gemini would be a silent no-op at run
    // time — that's the bug this hiding logic is meant to prevent.
    renderPane([makeRuntime("gemini")]);
    expect(
      screen.queryByRole("button", { name: /^MCP$/i }),
    ).not.toBeInTheDocument();
  });

  it("keeps the MCP tab visible when the runtime row hasn't loaded yet", () => {
    // Empty runtimes[] mimics the brief window between the page mounting and
    // the runtimes query resolving. Hiding the tab would flicker it off and
    // then back on, which reads as a bug.
    renderPane([]);
    expect(screen.getByRole("button", { name: /^MCP$/i })).toBeInTheDocument();
  });
});

describe("AgentOverviewPane 集成 tab visibility", () => {
  it("shows the 集成 tab once the deployment has Lark configured", async () => {
    larkListingRef.current = { installations: [], configured: true };
    renderPane([makeRuntime("claude")]);
    expect(
      await screen.findByRole("button", { name: /^集成$/i }),
    ).toBeInTheDocument();
  });

  it("hides the 集成 tab when Lark is not configured", () => {
    // Default ref is configured:false; the tab must not appear on
    // deployments without the integration, which are the common case.
    renderPane([makeRuntime("claude")]);
    expect(
      screen.queryByRole("button", { name: /^集成$/i }),
    ).not.toBeInTheDocument();
  });
});

describe("AgentOverviewPane unsaved navigation guard", () => {
  it("routes a sibling navigation intent through the dirty-tab confirmation", () => {
    const onNavIntentHandled = vi.fn();
    const { rerenderPane } = renderPane([makeRuntime("claude")], {
      navIntent: null,
      onNavIntentHandled,
    });

    fireEvent.click(screen.getByRole("button", { name: "指令" }));
    fireEvent.click(screen.getByRole("button", { name: "mark-instructions-dirty" }));
    rerenderPane({ navIntent: "skills", onNavIntentHandled });

    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    expect(screen.getByText("放弃未保存的修改？")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "继续编辑" }));
    expect(screen.getByRole("button", { name: "mark-instructions-dirty" })).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(onNavIntentHandled).toHaveBeenCalledTimes(1);
  });
});
