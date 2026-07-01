// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react";
import type { Agent, MemberWithUser, RuntimeDevice } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import enCommon from "../../locales/zh-Hans/common.json";
import enAgents from "../../locales/zh-Hans/agents.json";

const navigationStub: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/",
  searchParams: new URLSearchParams(),
  getShareableUrl: (path: string) => path,
};

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, agents: enAgents } };

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

// ModelDropdown talks to the api; the create dialog only needs it as a
// stand-in here, so swap it out.
vi.mock("./model-dropdown", () => ({
  ModelDropdown: () => null,
}));

// Provider logos don't matter for these assertions but they pull in SVGs.
vi.mock("../../runtimes/components/provider-logo", () => ({
  ProviderLogo: () => null,
}));

// Avatars hit the api for member metadata.
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => null,
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import { CreateAgentDialog } from "./create-agent-dialog";

const ME = "user-me";
const OTHER = "user-other";

const members: MemberWithUser[] = [
  {
    id: "m-me",
    user_id: ME,
    workspace_id: "ws-1",
    role: "member",
    name: "Me",
    account: "me",
    avatar_url: null,
    created_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "m-other",
    user_id: OTHER,
    workspace_id: "ws-1",
    role: "member",
    name: "其他",
    account: "other",
    avatar_url: null,
    created_at: "2026-01-01T00:00:00Z",
  },
];

function makeRuntime(overrides: Partial<RuntimeDevice>): RuntimeDevice {
  return {
    id: "rt",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Test Runtime",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "host.local",
    metadata: {},
    owner_id: ME,
    scope: "personal",
    last_seen_at: "2026-04-27T11:59:50Z",
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
    ...overrides,
  };
}

function makeTemplate(runtimeId: string): Agent {
  return {
    id: "agent-template",
    workspace_id: "ws-1",
    runtime_id: runtimeId,
    name: "Template Agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    scope: "personal",
    status: "idle",
    max_concurrent_tasks: 1,
    model: "",
    owner_id: ME,
    skills: [],
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
  };
}

function renderDialog(runtimes: RuntimeDevice[], template?: Agent) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const onCreate = vi.fn().mockResolvedValue(undefined);
  const onClose = vi.fn();
  render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <WorkspaceSlugProvider slug="test-ws">
        <NavigationProvider value={navigationStub}>
          <CreateAgentDialog
            runtimes={runtimes}
            members={members}
            currentUserId={ME}
            template={template}
            onClose={onClose}
            onCreate={onCreate}
          />
        </NavigationProvider>
        </WorkspaceSlugProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { onCreate, onClose };
}

describe("CreateAgentDialog runtime scope gate", () => {
  beforeEach(() => vi.clearAllMocks());
  // Base UI Dialog renders into a portal on document.body and leaves
  // focus-guard / inert wrapper divs around after the React tree unmounts.
  // The auto-cleanup from @testing-library/react drops the container but
  // not the portal residue, so two-tests-in-a-row queries see double
  // matches ("全部", "My Runtime"). Force cleanup + wipe body between tests.
  afterEach(() => {
    cleanup();
    document.body.innerHTML = "";
  });

  it("disables another member's personal runtime in the picker", () => {
    const mine = makeRuntime({ id: "rt-mine", name: "My Runtime", owner_id: ME, scope: "personal" });
    const othersPrivate = makeRuntime({
      id: "rt-others-private",
      name: "Others Private",
      owner_id: OTHER,
      scope: "personal",
    });
    renderDialog([mine, othersPrivate]);

    // Flip to "全部" so other-owned runtimes show.
    fireEvent.click(screen.getByText("全部"));
    // Open the picker.
    fireEvent.click(
      screen.getByText("My Runtime", { selector: "span.truncate" }),
    );

    const disabledRow = screen
      .getByText("Others Private")
      .closest("button") as HTMLButtonElement;
    expect(disabledRow).not.toBeNull();
    expect(disabledRow.disabled).toBe(true);
    expect(disabledRow.title).toMatch(/个人运行时/i);
  });

  it("does not let a personal agent use a workspace runtime", () => {
    const mine = makeRuntime({ id: "rt-mine", name: "My Runtime", owner_id: ME, scope: "personal" });
    const workspaceRuntime = makeRuntime({
      id: "rt-workspace",
      name: "Workspace Runtime",
      owner_id: OTHER,
      scope: "workspace",
    });
    renderDialog([mine, workspaceRuntime]);

    fireEvent.click(screen.getByText("全部"));
    fireEvent.click(
      screen.getByText("My Runtime", { selector: "span.truncate" }),
    );

    const workspaceRow = screen
      .getByText("Workspace Runtime")
      .closest("button") as HTMLButtonElement;
    expect(workspaceRow).not.toBeNull();
    expect(workspaceRow.disabled).toBe(true);
  });

  it("lets a workspace agent use a workspace runtime", async () => {
    const mine = makeRuntime({ id: "rt-mine", name: "My Runtime", owner_id: ME, scope: "personal" });
    const workspaceRuntime = makeRuntime({
      id: "rt-workspace",
      name: "Workspace Runtime",
      owner_id: OTHER,
      scope: "workspace",
    });
    renderDialog([mine, workspaceRuntime]);

    fireEvent.click(screen.getByText("全部"));
    fireEvent.click(screen.getByText("工作区"));
    await waitFor(() =>
      expect(
        screen.getByText("Workspace Runtime", { selector: "span.truncate" }),
      ).toBeInTheDocument(),
    );
    fireEvent.click(
      screen.getByText("Workspace Runtime", { selector: "span.truncate" }),
    );

    const workspaceButtons = screen
      .getAllByText("Workspace Runtime")
      .map((node) => node.closest("button") as HTMLButtonElement | null)
      .filter((button): button is HTMLButtonElement => button != null);
    expect(workspaceButtons.length).toBeGreaterThan(0);
    expect(workspaceButtons.every((button) => !button.disabled)).toBe(true);
  });

  it("defaults the selected runtime to a usable one, not a locked personal runtime", () => {
    const othersPrivate = makeRuntime({
      id: "rt-others-private",
      name: "Others Private",
      owner_id: OTHER,
      scope: "personal",
    });
    const mine = makeRuntime({
      id: "rt-mine",
      name: "My Runtime",
      owner_id: ME,
      scope: "personal",
    });
    renderDialog([othersPrivate, mine]);

    // The trigger label shows the selected runtime name. The picker must
    // not seed with the other-owned personal runtime even if it sorted
    // first in the input list.
    expect(screen.queryByText("Others Private", { selector: "span.truncate" })).toBeNull();
    expect(screen.getByText("My Runtime", { selector: "span.truncate" })).toBeInTheDocument();
  });

  it("in duplicate mode, does not pre-fill the template's runtime when it's now locked", async () => {
    // Template runtime is owned by someone else and now personal — the
    // duplicate flow used to seed with it anyway, leaving the user with
    // a 创建 button that 403s server-side. Now we fall back to the
    // first usable runtime instead.
    const othersPrivate = makeRuntime({
      id: "rt-others-private",
      name: "Others Private",
      owner_id: OTHER,
      scope: "personal",
    });
    const mine = makeRuntime({
      id: "rt-mine",
      name: "My Runtime",
      owner_id: ME,
      scope: "personal",
    });
    const template = makeTemplate("rt-others-private");
    const { onCreate } = renderDialog([othersPrivate, mine], template);

    expect(
      screen.getByText("My Runtime", { selector: "span.truncate" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Others Private", { selector: "span.truncate" }),
    ).toBeNull();

    // Sanity check: with a usable selection seeded, 创建 should submit.
    fireEvent.click(screen.getByText("创建"));
    await new Promise((r) => setTimeout(r, 0));
    expect(onCreate).toHaveBeenCalledTimes(1);
    expect(onCreate.mock.calls[0]?.[0].runtime_id).toBe("rt-mine");
  });

  it("disables 创建 when the selected runtime is locked (template + no usable fallback)", () => {
    // Edge case: template points at a locked runtime AND the workspace
    // has no usable alternatives in scope. The defense-in-depth gate on
    // the 创建 button must keep the user from submitting a 403.
    const onlyOthersPrivate = makeRuntime({
      id: "rt-only-others-private",
      name: "Only Others Private",
      owner_id: OTHER,
      scope: "personal",
    });
    // Flip the picker to "全部" so the locked runtime is at least
    // visible — that's the scope where the selected-but-locked state
    // can persist after the initial seed search returns nothing.
    const template = makeTemplate("rt-only-others-private");
    renderDialog([onlyOthersPrivate], template);

    // The 创建 button is rendered by lucide-free CTA text "创建".
    const createBtn = screen
      .getAllByRole("button")
      .find((b) => b.textContent === "创建");
    expect(createBtn).toBeDefined();
    expect((createBtn as HTMLButtonElement).disabled).toBe(true);
  });
});
