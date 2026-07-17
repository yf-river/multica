// @vitest-environment jsdom

import type { ReactNode } from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { screen, fireEvent, waitFor } from "@testing-library/react";
import type { Agent, MemberWithUser, Squad } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";

const ME = "user-me";
const OTHER = "user-other";

const mocks = vi.hoisted(() => ({
  agents: [] as Agent[],
  members: [] as MemberWithUser[],
  createSquad: vi.fn(),
  navigationPush: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
  invalidate: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey?: unknown[] }) => {
    const key = opts.queryKey ?? [];
    if (Array.isArray(key) && key.includes("agents")) {
      return { data: mocks.agents };
    }
    if (Array.isArray(key) && key.includes("members")) {
      return { data: mocks.members };
    }
    return { data: [] };
  },
  useQueryClient: () => ({ invalidateQueries: mocks.invalidate }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({ queryKey: ["agents"] }),
  memberListOptions: () => ({ queryKey: ["members"] }),
  workspaceKeys: { squads: (id: string) => ["squads", id] },
}));

vi.mock("@multica/core/squads", () => ({
  useCreateSquad: () => ({
    mutateAsync: (...args: unknown[]) => mocks.createSquad(...args),
    isPending: false,
  }),
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (s: { user: { id: string } | null }) => unknown) =>
    selector({ user: { id: ME } }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspaceId: () => "ws-1",
  useWorkspacePaths: () => ({
    squadDetail: (id: string) => `/test-ws/squads/${id}`,
  }),
}));

vi.mock("@multica/core/utils", () => ({
  isImeComposing: () => false,
}));

vi.mock("../navigation", () => ({
  useNavigation: () => ({ push: mocks.navigationPush }),
}));

vi.mock("../common/actor-avatar", () => ({
  ActorAvatar: ({ actorId }: { actorId: string }) => (
    <span data-testid={`avatar-${actorId}`} />
  ),
}));

vi.mock("../agents/components/avatar-picker", () => ({
  AvatarPicker: ({
    value,
    onChange,
  }: {
    value: string | null;
    onChange: (v: string | null) => void;
  }) => (
    <button
      type="button"
      data-testid="avatar-picker"
      data-value={value ?? ""}
      onClick={() => onChange("https://example.com/avatar.png")}
    >
      avatar
    </button>
  ),
}));

vi.mock("../agents/components/char-counter", () => ({
  CharCounter: ({ length, max }: { length: number; max: number }) => (
    <span data-testid="char-counter">
      {length}/{max}
    </span>
  ),
}));

vi.mock("@multica/ui/components/ui/popover", () => ({
  Popover: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  PopoverTrigger: ({
    children,
    className,
    onClick,
    render,
  }: {
    children?: ReactNode;
    className?: string;
    onClick?: () => void;
    render?: ReactNode;
  }) => {
    if (render !== undefined) return <>{render}</>;
    return (
      <button type="button" className={className} onClick={onClick}>
        {children}
      </button>
    );
  },
  PopoverContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

vi.mock("@multica/ui/components/ui/dialog", () => ({
  Dialog: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogHeader: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: ReactNode }) => <h2>{children}</h2>,
  DialogDescription: ({ children }: { children: ReactNode }) => <p>{children}</p>,
}));

vi.mock("@multica/ui/components/ui/button", () => ({
  Button: ({
    children,
    disabled,
    onClick,
    type = "button",
  }: {
    children: ReactNode;
    disabled?: boolean;
    onClick?: () => void;
    type?: "button" | "submit" | "reset";
  }) => (
    <button type={type} disabled={disabled} onClick={onClick}>
      {children}
    </button>
  ),
}));

vi.mock("@multica/ui/components/ui/input", () => ({
  Input: (props: React.InputHTMLAttributes<HTMLInputElement>) => (
    <input {...props} />
  ),
}));

vi.mock("@multica/ui/components/ui/label", () => ({
  Label: ({ children, className }: { children: ReactNode; className?: string }) => (
    <label className={className}>{children}</label>
  ),
}));

vi.mock("../editor/extensions/pinyin-match", () => ({
  matchesTextQuery: () => true,
}));

vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => mocks.toastSuccess(...args),
    error: (...args: unknown[]) => mocks.toastError(...args),
  },
}));

import { CreateSquadModal } from "./create-squad";

function makeAgent(overrides: Partial<Agent> & { id: string; name: string; owner_id: string | null }): Agent {
  return {
    runtime_id: "rt-1",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    custom_env_key_count: 0,
    mcp_config: null,
    mcp_config_redacted: false,
    scope: "personal",
    max_concurrent_tasks: 1,
    model: "",
    thinking_level: "",
    skills: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    archived_at: null,
    ...overrides,
  };
}

function makeMember(user_id: string, name: string): MemberWithUser {
  return {
    id: `m-${user_id}`,
    user_id,
    role: "member",
    name,
    account: user_id,
    avatar_url: null,
  };
}

function makeSquad(overrides: Partial<Squad> = {}): Squad {
  return {
    id: "sq-new",
    name: "New Squad",
    description: "",
    instructions: "",
    avatar_url: null,
    scope: "workspace",
    leader_id: "agent-mine-1",
    creator_id: ME,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    archived_at: null,
    ...overrides,
  };
}

function getSubmitButton(): HTMLButtonElement {
  const btn = screen
    .getAllByRole("button")
    .find((b) => b.textContent === "创建小队");
  if (!btn) throw new Error("创建小队 submit button not found");
  return btn as HTMLButtonElement;
}

function match(label: string, position: "first" | "last"): HTMLElement {
  const matches = screen.getAllByText(label);
  if (matches.length === 0) throw new Error(`no match for "${label}"`);
  return position === "first" ? matches[0]! : matches[matches.length - 1]!;
}

function submitSquad(name: string) {
  fireEvent.change(screen.getByPlaceholderText(/例如 前端团队/i), {
    target: { value: name },
  });
  fireEvent.click(getSubmitButton());
}

async function addSecondAgent() {
  fireEvent.click(match("MineAgentTwo", "last"));
  await waitFor(() => {
    expect(screen.getAllByText("MineAgentTwo").length).toBeGreaterThanOrEqual(2);
  });
}

function renderModal() {
  const onClose = vi.fn();
  renderWithI18n(<CreateSquadModal onClose={onClose} />);
  return { onClose };
}

const myAgent = makeAgent({ id: "agent-mine-1", name: "MineAgentOne", owner_id: ME });
const myAgent2 = makeAgent({ id: "agent-mine-2", name: "MineAgentTwo", owner_id: ME });
const otherAgent = makeAgent({ id: "agent-other-1", name: "OtherAgentOne", owner_id: OTHER });
const wsMember = makeMember(OTHER, "Workspace Pal");

describe("CreateSquadModal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.agents = [otherAgent, myAgent, myAgent2];
    mocks.members = [wsMember];
  });

  it("binds the name and description inputs in the identity row", () => {
    renderModal();
    const name = screen.getByPlaceholderText(/例如 前端团队/i) as HTMLInputElement;
    fireEvent.change(name, { target: { value: "Platform Team" } });
    expect(name.value).toBe("Platform Team");

    const desc = screen.getByPlaceholderText(/描述这个小队/i) as HTMLInputElement;
    fireEvent.change(desc, { target: { value: "We own infra" } });
    expect(desc.value).toBe("We own infra");
    expect(screen.getByTestId("char-counter").textContent).toMatch(/^12\//);
  });

  it("renders the leader picker with 'My Agents' before 'Workspace Agents'", () => {
    renderModal();
    const myGroupLabels = screen.getAllByText("我的智能体");
    const wsGroupLabels = screen.getAllByText("工作区智能体");
    expect(myGroupLabels.length).toBeGreaterThanOrEqual(1);
    expect(wsGroupLabels.length).toBeGreaterThanOrEqual(1);

    const all = Array.from(document.querySelectorAll("*"));
    const myIdx = all.findIndex((n) => n.textContent === "我的智能体");
    const wsIdx = all.findIndex((n) => n.textContent === "工作区智能体");
    expect(myIdx).toBeLessThan(wsIdx);
  });

  it("auto-clears an additional-members entry when the same agent is picked as leader", async () => {
    renderModal();
    await addSecondAgent();

    fireEvent.click(match("MineAgentTwo", "first"));

    mocks.createSquad.mockResolvedValue(makeSquad({ leader_id: "agent-mine-2" }));
    submitSquad("Platform");

    await waitFor(() => {
      expect(mocks.createSquad).toHaveBeenCalledTimes(1);
    });
    expect(mocks.createSquad).toHaveBeenCalledWith(
      expect.objectContaining({ members: [] }),
    );
  });

  it("removes a member promoted to leader from selectedMembers so switching leader away does not resurrect it", async () => {
    renderModal();
    await addSecondAgent();

    fireEvent.click(match("MineAgentTwo", "first"));
    fireEvent.click(match("MineAgentOne", "first"));

    mocks.createSquad.mockResolvedValue(makeSquad({ id: "sq-3", leader_id: "agent-mine-1" }));
    submitSquad("Swap Squad");

    await waitFor(() => {
      expect(mocks.createSquad).toHaveBeenCalledWith({
        name: "Swap Squad",
        description: undefined,
        leader_id: "agent-mine-1",
        avatar_url: undefined,
        scope: "workspace",
        members: [],
      });
    });
  });

  it("on success with no additional members fires exactly one success toast and navigates", async () => {
    renderModal();
    fireEvent.click(match("MineAgentOne", "first"));

    mocks.createSquad.mockResolvedValue(makeSquad({ id: "sq-1", leader_id: "agent-mine-1" }));

    submitSquad("Solo Squad");

    await waitFor(() => {
      expect(mocks.createSquad).toHaveBeenCalledWith({
        name: "Solo Squad",
        description: undefined,
        leader_id: "agent-mine-1",
        avatar_url: undefined,
        scope: "workspace",
        members: [],
      });
    });
    await waitFor(() => {
      expect(mocks.toastSuccess).toHaveBeenCalledTimes(1);
    });
    expect(mocks.navigationPush).toHaveBeenCalledWith("/test-ws/squads/sq-1");
  });

  it("creates the squad and all initial members with one request", async () => {
    renderModal();
    fireEvent.click(match("MineAgentOne", "first"));
    fireEvent.click(match("OtherAgentOne", "last"));
    fireEvent.click(match("Workspace Pal", "last"));

    mocks.createSquad.mockResolvedValue(makeSquad({ id: "sq-2", leader_id: "agent-mine-1" }));

    submitSquad("Mixed Squad");

    await waitFor(() => {
      expect(mocks.createSquad).toHaveBeenCalledWith({
        name: "Mixed Squad",
        description: undefined,
        leader_id: "agent-mine-1",
        avatar_url: undefined,
        scope: "workspace",
        members: [
          { member_type: "agent", member_id: "agent-other-1" },
          { member_type: "member", member_id: "user-other" },
        ],
      });
    });
    await waitFor(() => {
      expect(mocks.toastSuccess).toHaveBeenCalledTimes(1);
    });
    expect(mocks.navigationPush).toHaveBeenCalledWith("/test-ws/squads/sq-2");
  });

  it("submits personal scope when selected", async () => {
    renderModal();
    fireEvent.click(screen.getByText("个人"));
    fireEvent.click(match("MineAgentOne", "first"));
    mocks.createSquad.mockResolvedValue(makeSquad({ id: "sq-personal", scope: "personal" }));

    submitSquad("Personal Squad");

    await waitFor(() => {
      expect(mocks.createSquad).toHaveBeenCalledWith({
        name: "Personal Squad",
        description: undefined,
        leader_id: "agent-mine-1",
        avatar_url: undefined,
        scope: "personal",
        members: [],
      });
    });
  });

  it("on createSquad failure shows an error toast, does not navigate, and re-enables submit", async () => {
    renderModal();
    fireEvent.click(match("MineAgentOne", "first"));

    mocks.createSquad.mockRejectedValueOnce(new Error("server down"));

    submitSquad("Boom Squad");

    await waitFor(() => {
      expect(mocks.toastError).toHaveBeenCalledTimes(1);
    });
    expect(mocks.navigationPush).not.toHaveBeenCalled();
    const button = getSubmitButton();
    expect(button.disabled).toBe(false);
  });
});
