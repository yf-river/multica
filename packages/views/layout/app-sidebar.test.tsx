import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@multica/core/api";
import { AppSidebar } from "./app-sidebar";

const { detail, deletePin, navigation, pins } = vi.hoisted(() => ({
  detail: { current: { isPending: false, isError: false, data: null as unknown, error: null as unknown } },
  deletePin: vi.fn(),
  navigation: { current: { pathname: "/acme/issues", searchParams: new URLSearchParams() } },
  pins: {
    current: [
      {
        id: "pin-1",
        item_type: "issue" as const,
        item_id: "issue-1",
      },
    ],
  },
}));

vi.mock("@dnd-kit/core", () => ({
  DndContext: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  PointerSensor: vi.fn(),
  closestCenter: vi.fn(),
  useSensor: vi.fn(),
  useSensors: vi.fn(),
}));
vi.mock("@dnd-kit/sortable", () => ({
  SortableContext: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  useSortable: () => ({ attributes: {}, listeners: {}, setNodeRef: vi.fn() }),
  verticalListSortingStrategy: vi.fn(),
}));
vi.mock("@dnd-kit/utilities", () => ({ CSS: { Transform: { toString: () => undefined } } }));
vi.mock("@multica/ui/components/ui/sidebar", () => ({
  Sidebar: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarFooter: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarGroup: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarGroupContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarGroupLabel: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  SidebarHeader: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarMenu: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarMenuButton: ({
    children,
    isActive,
    render,
  }: {
    children: React.ReactNode;
    isActive?: boolean;
    render?: React.ReactElement<{ href?: string }>;
  }) => (
    <button type="button" data-active={isActive ? "true" : undefined} data-href={render?.props.href}>
      {children}
    </button>
  ),
  SidebarMenuItem: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarRail: () => null,
}));
vi.mock("@multica/ui/components/ui/dropdown-menu", () => ({
  DropdownMenu: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuGroup: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuItem: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuLabel: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuSeparator: () => null,
  DropdownMenuTrigger: ({ render }: { render: React.ReactNode }) => <>{render}</>,
}));
vi.mock("@multica/ui/components/ui/collapsible", () => ({
  Collapsible: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  CollapsibleContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  CollapsibleTrigger: () => <button type="button" />,
}));
vi.mock("@multica/ui/components/ui/tooltip", () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ children }: { children: React.ReactNode }) => <button type="button">{children}</button>,
}));
vi.mock("./help-launcher", () => ({ HelpLauncher: () => null }));
vi.mock("../auth", () => ({ useLogout: () => vi.fn() }));
vi.mock("../issues/components/status-icon", () => ({ StatusIcon: () => <span /> }));
vi.mock("../navigation", () => ({
  AppLink: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a>,
  useNavigation: () => ({ pathname: navigation.current.pathname, searchParams: navigation.current.searchParams, push: vi.fn() }),
}));
vi.mock("../projects/components/project-icon", () => ({ ProjectIcon: () => <span /> }));
vi.mock("../workspace/workspace-avatar", () => ({ WorkspaceAvatar: () => <span /> }));
vi.mock("@multica/ui/components/common/actor-avatar", () => ({ ActorAvatar: () => <span /> }));
vi.mock("../i18n", () => ({
  useT: () => ({
    t: (sel: (s: any) => string) =>
      sel({
        nav: {
          companion: "搭子",
          life: "人生",
          life_memory: "记忆",
          life_experiment: "实验",
          life_observers: "观察席",
          life_chronicle: "编年史",
          inbox: "收件箱",
          my_issues: "我的 issue",
          issues: "任务",
          projects: "项目",
          autopilots: "自动化",
          agents: "智能体",
          squads: "小队",
          usage: "用量",
          run_reviews: "运行复盘",
          runtimes: "运行时",
          skills: "技能",
          settings: "设置",
        },
        sidebar: {
          unpin_tooltip: "取消固定",
          workspaces_label: "工作区",
          create_workspace: "创建工作区",
          log_out: "退出登录",
          new_issue: "新建任务",
          new_issue_shortcut: "C",
          pinned_label: "固定",
          workspace_group: "工作区",
          configure_group: "配置",
        },
      }),
  }),
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string; account: string; name: string } }) => unknown) =>
    selector({ user: { id: "user-1", account: "test", name: "Test User" } }),
}));
vi.mock("@multica/core/paths", () => ({
  LIFE_TABS: ["memory", "experiment", "observers", "chronicle"],
  paths: { workspace: (slug: string) => ({ issues: () => `/${slug}/issues` }) },
  useCurrentWorkspace: () => ({ id: "ws-1", name: "Acme", slug: "acme" }),
  useWorkspacePaths: () => ({
    companion: () => "/acme/companion",
    life: () => "/acme/life",
    lifeTab: (tab: string) => `/acme/life?tab=${tab}`,
    inbox: () => "/acme/inbox",
    myIssues: () => "/acme/my-issues",
    issues: () => "/acme/issues",
    projects: () => "/acme/projects",
    autopilots: () => "/acme/autopilots",
	    agents: () => "/acme/agents",
	    squads: () => "/acme/squads",
	    usage: () => "/acme/usage",
	    runReviews: () => "/acme/run-reviews",
	    runtimes: () => "/acme/runtimes",
    skills: () => "/acme/skills",
    settings: () => "/acme/settings",
    issueDetail: (id: string) => `/acme/issues/${id}`,
    projectDetail: (id: string) => `/acme/projects/${id}`,
  }),
}));
vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getBaseUrl: () => "http://127.0.0.1:8080",
    },
  };
});
vi.mock("@multica/core/inbox/queries", () => ({ deduplicateInboxItems: (items: unknown[]) => items, inboxKeys: { list: () => ["inbox"] } }));
vi.mock("@multica/core/issues/queries", () => ({ issueDetailOptions: () => ({ queryKey: ["issue"] }) }));
vi.mock("@multica/core/issues", () => ({ openCreateIssue: vi.fn() }));
vi.mock("@multica/core/issues/stores/draft-store", () => ({ useIssueDraftStore: () => false }));
vi.mock("@multica/core/modals", () => ({ useModalStore: { getState: () => ({ modal: null, open: vi.fn() }) } }));
vi.mock("@multica/core/pins/mutations", () => ({ useDeletePin: () => ({ mutate: deletePin }), useReorderPins: () => ({ mutate: vi.fn() }) }));
vi.mock("@multica/core/pins/queries", () => ({ pinListOptions: () => ({ queryKey: ["pins"] }) }));
vi.mock("@multica/core/projects/queries", () => ({ projectDetailOptions: () => ({ queryKey: ["project"] }) }));
vi.mock("@multica/core/workspace/queries", () => ({
  workspaceListOptions: () => ({ queryKey: ["workspaces"] }),
}));
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useMutation: () => ({ isPending: false, mutate: vi.fn() }),
  useQuery: ({ queryKey }: { queryKey: readonly unknown[] }) => {
    if (queryKey[0] === "pins") return { data: pins.current };
    if (queryKey[0] === "issue") return detail.current;
    return { data: [] };
  },
  useQueryClient: () => ({ fetchQuery: vi.fn(), invalidateQueries: vi.fn() }),
}));

describe("PinRow", () => {
  beforeEach(() => {
    deletePin.mockReset();
    navigation.current.pathname = "/acme/issues";
    detail.current = { isPending: false, isError: false, data: null, error: null };
  });

  it("unpins missing details", async () => {
    detail.current = { isPending: false, isError: true, data: null, error: new ApiError("missing", 404, "Not Found") };
    render(<AppSidebar />);
    await waitFor(() => expect(deletePin).toHaveBeenCalledTimes(1));
  });

  it("ignores non-404 errors", async () => {
    detail.current = { isPending: false, isError: true, data: null, error: new ApiError("error", 500, "Server Error") };
    render(<AppSidebar />);
    await waitFor(() => expect(deletePin).not.toHaveBeenCalled());
  });

  it("renders loaded details", async () => {
    detail.current = { isPending: false, isError: false, data: { identifier: "MUL-123", title: "Keep this pin", status: "todo" }, error: null };
    render(<AppSidebar />);
    expect(await screen.findByText("MUL-123 Keep this pin")).toBeInTheDocument();
  });

  it("shows the account label in the user menu instead of promoting the English name", async () => {
    render(<AppSidebar />);

    expect(await screen.findByText("账号 test")).toBeInTheDocument();
    expect(screen.getByText("Test User")).toBeInTheDocument();
  });

  it("does not also highlight the parent workspace nav for an active pin", async () => {
    navigation.current.pathname = "/acme/issues/issue-1";
    detail.current = {
      isPending: false,
      isError: false,
      data: { identifier: "MUL-123", title: "Keep this pin", status: "todo" },
      error: null,
    };

    const { container } = render(<AppSidebar />);

    expect((await screen.findByText("MUL-123 Keep this pin")).closest("button")).toHaveAttribute(
      "data-active",
      "true",
    );
    expect(container.querySelector('button[data-href="/acme/issues"]')).not.toHaveAttribute("data-active");
  });
});

describe("AppSidebar workspace nav", () => {
  beforeEach(() => {
    navigation.current.pathname = "/acme/issues";
    navigation.current.searchParams = new URLSearchParams();
    detail.current = { isPending: false, isError: false, data: null, error: null };
  });

  it("renders 运行复盘 as the canonical workspace run review entry", () => {
    render(<AppSidebar />);

    expect(screen.getByText("运行复盘")).toBeInTheDocument();
    expect(document.querySelector('[data-href="/acme/run-reviews"]')).toHaveAttribute("data-href", "/acme/run-reviews");
    expect(screen.queryByText("用量")).not.toBeInTheDocument();
  });

});

describe("AppSidebar personal nav", () => {
  it("keeps the companion in the chat launcher instead of the sidebar", () => {
    render(<AppSidebar />);

    expect(screen.queryByText("搭子")).not.toBeInTheDocument();
    expect(screen.getByText("记忆")).toBeInTheDocument();
  });
});

describe("AppSidebar life nav", () => {
  beforeEach(() => {
    navigation.current.pathname = "/acme/life";
    navigation.current.searchParams = new URLSearchParams();
    detail.current = { isPending: false, isError: false, data: null, error: null };
  });

  it("renders the four life tabs as query links", () => {
    const { container } = render(<AppSidebar />);

    expect(container.textContent).toContain("人生");
    const tabs = ["memory", "experiment", "observers", "chronicle"] as const;
    for (const tab of tabs) {
      expect(container.querySelector(`button[data-href="/acme/life?tab=${tab}"]`)).toBeInTheDocument();
    }
    expect(container.querySelector('button[data-href="/acme/life"]')).not.toBeInTheDocument();
    expect(
      [...container.querySelectorAll<HTMLButtonElement>('button[data-href^="/acme/life?tab="]')].map(
        (button) => button.dataset.href,
      ),
    ).toEqual(tabs.map((tab) => `/acme/life?tab=${tab}`));
  });

  it("activates the tab from the URL and defaults to memory", () => {
    const { container, rerender } = render(<AppSidebar />);

    expect(container.querySelector('button[data-href="/acme/life?tab=memory"]')).toHaveAttribute(
      "data-active",
      "true",
    );
    expect(container.querySelector('button[data-href="/acme/life?tab=experiment"]')).not.toHaveAttribute("data-active");

    navigation.current.searchParams = new URLSearchParams("tab=observers");
    rerender(<AppSidebar />);
    expect(container.querySelector('button[data-href="/acme/life?tab=observers"]')).toHaveAttribute(
      "data-active",
      "true",
    );
    expect(container.querySelector('button[data-href="/acme/life?tab=memory"]')).not.toHaveAttribute("data-active");

    navigation.current.searchParams = new URLSearchParams("tab=unknown");
    rerender(<AppSidebar />);
    expect(container.querySelector('button[data-href="/acme/life?tab=memory"]')).toHaveAttribute(
      "data-active",
      "true",
    );
  });
});
