import { act, type ReactNode } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { SearchCommand } from "./search-command";
import { useSearchStore } from "@multica/core/search";
import enCommon from "../locales/zh-Hans/common.json";
import enAuth from "../locales/zh-Hans/auth.json";
import enSettings from "../locales/zh-Hans/settings.json";
import enSearch from "../locales/zh-Hans/search.json";

const TEST_RESOURCES = {
  "zh-Hans": { common: enCommon, auth: enAuth, settings: enSettings, search: enSearch },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

const renderSearch = () => render(<SearchCommand />, { wrapper: I18nWrapper });

async function renderSearchQuery(query: string) {
  const user = userEvent.setup();
  renderSearch();
  await user.type(screen.getByPlaceholderText("输入命令或关键词搜索..."), query);
  return user;
}

const {
  mockPush,
  mockSearchIssues,
  mockSearchProjects,
  mockRecentItems,
  mockAllIssues,
  mockSetTheme,
  mockTheme,
  mockPathname,
  mockGetShareableUrl,
  mockMembers,
  mockAgents,
  mockSquads,
  mockOpenModal,
  mockToastSuccess,
  mockClipboardWrite,
  mockUseQueryCalls,
  mockUseQueriesCalls,
} = vi.hoisted(() => ({
  mockPush: vi.fn(),
  mockSearchIssues: vi.fn(),
  mockSearchProjects: vi.fn(),
  mockRecentItems: {
    current: [] as Array<{ type: "issue" | "project"; id: string; visitedAt: number }>,
  },
  mockAllIssues: { current: [] as Array<Record<string, unknown>> },
  mockSetTheme: vi.fn(),
  mockTheme: { current: "system" as "light" | "dark" | "system" },
  mockPathname: { current: "/ws-test/issues" as string },
  mockGetShareableUrl: vi.fn((p: string) => `https://app.multica/${p}`),
  mockMembers: {
    current: [] as Array<{
      id: string;
      workspace_id: string;
      user_id: string;
      role: "owner" | "admin" | "member";
      created_at: string;
      name: string;
      account: string;
      avatar_url: string | null;
    }>,
  },
  mockAgents: {
    current: [] as Array<{
      id: string;
      name: string;
      avatar_url: string | null;
    }>,
  },
  mockSquads: {
    current: [] as Array<{
      id: string;
      name: string;
      avatar_url: string | null;
    }>,
  },
  mockOpenModal: vi.fn(),
  mockToastSuccess: vi.fn(),
  mockClipboardWrite: vi.fn(() => Promise.resolve()),
  mockUseQueryCalls: { current: [] as Array<{ queryKey: readonly unknown[]; enabled?: boolean }> },
  mockUseQueriesCalls: { current: [] as Array<{ queryKey: readonly unknown[] }> },
}));

vi.mock("@multica/core/api", () => ({
  api: {
    getBaseUrl: () => "http://127.0.0.1:8080",
    searchIssues: mockSearchIssues,
    searchProjects: mockSearchProjects,
  },
}));

vi.mock("../common/actor-avatar", () => ({
  ActorAvatar: ({
    actorType,
    actorId,
  }: {
    actorType: string;
    actorId: string;
  }) => {
    const name =
      actorType === "member"
        ? mockMembers.current.find((m) => m.user_id === actorId)?.name
        : actorType === "agent"
          ? mockAgents.current.find((a) => a.id === actorId)?.name
          : actorType === "squad"
            ? mockSquads.current.find((s) => s.id === actorId)?.name
            : undefined;
    return (
      <span
        data-testid="issue-assignee-avatar"
        title={name ?? `${actorType}:${actorId}`}
      />
    );
  },
}));

vi.mock("@multica/core/chat", () => {
  const EMPTY: typeof mockRecentItems.current = [];
  return {
    useRecentContextStore: (
      selector?: (state: {
        byWorkspace: Record<string, typeof mockRecentItems.current>;
      }) => unknown,
    ) => {
      const state = { byWorkspace: { "ws-test": mockRecentItems.current } };
      return selector ? selector(state) : state;
    },
    selectRecentContexts:
      (wsId: string | null) =>
      (state: { byWorkspace: Record<string, typeof mockRecentItems.current> }) =>
        wsId ? (state.byWorkspace[wsId] ?? EMPTY) : EMPTY,
  };
});

vi.mock("@multica/core/issues", () => ({
  openCreateIssue: (data?: Record<string, unknown> | null) =>
    mockOpenModal("create-issue", data ?? null),
}));

vi.mock("@multica/core", () => ({
  useWorkspaceId: () => "ws-test",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    inbox: () => "/ws-test/inbox",
    myIssues: () => "/ws-test/my-issues",
    issues: () => "/ws-test/issues",
	    projects: () => "/ws-test/projects",
	    agents: () => "/ws-test/agents",
	    debug: () => "/ws-test/debug",
	    debugView: (view: string) => `/ws-test/debug/${view}`,
	    evaluation: () => "/ws-test/evaluation",
	    evaluationView: (view: string) => `/ws-test/evaluation/${view}`,
	    runtimes: () => "/ws-test/runtimes",
    skills: () => "/ws-test/skills",
    settings: () => "/ws-test/settings",
    issueDetail: (id: string) => `/ws-test/issues/${id}`,
    memberDetail: (id: string) => `/ws-test/members/${id}`,
    agentDetail: (id: string) => `/ws-test/agents/${id}`,
    squadDetail: (id: string) => `/ws-test/squads/${id}`,
    projectDetail: (id: string) => `/ws-test/projects/${id}`,
  }),
}));

vi.mock("@multica/core/issues/queries", () => ({
  issueDetailOptions: (_wsId: string, id: string) => ({
    queryKey: ["issues", "ws-test", "detail", id],
  }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["workspaces", "ws-test", "members"] }),
  agentListOptions: () => ({ queryKey: ["workspaces", "ws-test", "agents"] }),
  squadListOptions: () => ({ queryKey: ["workspaces", "ws-test", "squads"] }),
}));

vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(vi.fn(), {
    getState: () => ({ open: mockOpenModal }),
  }),
}));

function resolveIssue(key: readonly unknown[]) {
  // issueDetailOptions key shape: ["issues", wsId, "detail", id]
  if (key[0] === "issues" && key[2] === "detail") {
    const id = key[3];
    return mockAllIssues.current.find((i) => i.id === id);
  }
  return undefined;
}

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey: readonly unknown[]; enabled?: boolean }) => {
    mockUseQueryCalls.current.push(opts);
    if (opts.enabled === false) return { data: undefined };
    const key = opts.queryKey;
    if (key[0] === "workspaces" && key[2] === "members") {
      return { data: mockMembers.current };
    }
    if (key[0] === "workspaces" && key[2] === "agents") {
      return { data: mockAgents.current };
    }
    if (key[0] === "workspaces" && key[2] === "squads") {
      return { data: mockSquads.current };
    }
    return { data: resolveIssue(key) };
  },
  useQueries: (opts: { queries: Array<{ queryKey: readonly unknown[] }> }) => {
    mockUseQueriesCalls.current.push(...opts.queries);
    return opts.queries.map((q) => ({ data: resolveIssue(q.queryKey) }));
  },
}));

vi.mock("../navigation", () => ({
  useNavigation: () => ({
    push: mockPush,
    pathname: mockPathname.current,
    getShareableUrl: mockGetShareableUrl,
  }),
}));

vi.mock("@multica/ui/components/common/theme-provider", () => ({
  useTheme: () => ({ theme: mockTheme.current, setTheme: mockSetTheme }),
}));

vi.mock("sonner", () => ({
  toast: { success: mockToastSuccess, error: vi.fn() },
}));

describe("SearchCommand", () => {
  beforeEach(() => {
    mockPush.mockReset();
    mockSearchIssues.mockReset().mockResolvedValue([]);
    mockSearchProjects.mockReset().mockResolvedValue([]);
    mockRecentItems.current = [];
    mockAllIssues.current = [];
    mockAgents.current = [];
    mockSquads.current = [];
    mockSetTheme.mockReset();
    mockTheme.current = "system";
    mockPathname.current = "/ws-test/issues";
    mockGetShareableUrl.mockReset().mockImplementation((p: string) => `https://app.multica/${p}`);
    mockMembers.current = [];
    mockOpenModal.mockReset();
    mockToastSuccess.mockReset();
    mockClipboardWrite.mockReset().mockResolvedValue(undefined);
    mockUseQueryCalls.current = [];
    mockUseQueriesCalls.current = [];

    // cmdk calls scrollIntoView on the first selected item, which jsdom doesn't implement
    Element.prototype.scrollIntoView = vi.fn();

    act(() => {
      useSearchStore.setState({ open: true });
    });
  });

  it("命令面板关闭时不预拉成员、最近任务和当前任务详情", () => {
    act(() => {
      useSearchStore.setState({ open: false });
    });
    mockRecentItems.current = [
      { type: "issue", id: "issue-1", visitedAt: 1000 },
      { type: "issue", id: "issue-2", visitedAt: 900 },
    ];
    mockPathname.current = "/ws-test/issues/issue-1";

    renderSearch();

    expect(screen.queryByPlaceholderText("输入命令或关键词搜索...")).not.toBeInTheDocument();
    expect(mockUseQueriesCalls.current).toHaveLength(0);
    expect(
      mockUseQueryCalls.current.some(
        (call) => call.queryKey[0] === "workspaces" && call.queryKey[2] === "members" && call.enabled !== false,
      ),
    ).toBe(false);
    expect(
      mockUseQueryCalls.current.some(
        (call) => call.queryKey[0] === "issues" && call.queryKey[2] === "detail" && call.enabled !== false,
      ),
    ).toBe(false);
  });

  it("从搜索输入框按一次 Escape 会关闭命令面板", async () => {
    const user = userEvent.setup();

    renderSearch();

    const input = screen.getByPlaceholderText("输入命令或关键词搜索...");
    await user.click(input);

    expect(useSearchStore.getState().open).toBe(true);

    await user.keyboard("{Escape}");

    await waitFor(() => {
      expect(useSearchStore.getState().open).toBe(false);
    });
    expect(screen.queryByPlaceholderText("输入命令或关键词搜索...")).not.toBeInTheDocument();
  });

  it("默认只显示新建任务，并在输入查询前隐藏页面和低频命令", () => {
    renderSearch();

    expect(screen.queryByText("页面")).not.toBeInTheDocument();
    // Only the primary creation action surfaces on empty query; everything
    // 其他命令（主题、复制、新建项目）必须输入查询后才显示。
    expect(screen.getByText("命令")).toBeInTheDocument();
    expect(
      screen.getByText((_, el) => el?.textContent === "新建任务" && el?.tagName === "SPAN"),
    ).toBeInTheDocument();
    expect(screen.queryByText("新建项目")).not.toBeInTheDocument();
    expect(screen.queryByText("切换到浅色主题")).not.toBeInTheDocument();
    expect(screen.queryByText("切换到深色主题")).not.toBeInTheDocument();
    expect(screen.queryByText("跟随系统主题")).not.toBeInTheDocument();
  });

  it("按查询过滤导航页面", async () => {
    await renderSearchQuery("set");

    await waitFor(() => {
      // HighlightText splits text, so use a function matcher
      expect(screen.getByText((_, el) => el?.textContent === "设置" && el?.tagName === "SPAN")).toBeInTheDocument();
    });
    expect(screen.queryByText("收件箱")).not.toBeInTheDocument();
  });

  it.each([
    ["设置页面", "settings", "page:settings", "/ws-test/settings"],
    ["调试默认入口", "调试", "page:debug", "/ws-test/debug/prompts"],
    ["评估默认入口", "评估", "page:evaluation", "/ws-test/evaluation/datasets"],
    ["提示词库", "提示词库", "command:training-prompts", "/ws-test/debug/prompts"],
    ["用例库", "用例库", "command:training-datasets", "/ws-test/evaluation/datasets"],
    ["测试套件", "测试套件", "command:training-test-suites", "/ws-test/evaluation/test-suites"],
    ["评测记录", "评测记录", "command:training-evaluation-runs", "/ws-test/evaluation/runs"],
  ])("选择%s后跳转并关闭面板", async (_name, query, value, href) => {
    const user = await renderSearchQuery(query);
    await waitFor(() => {
      expect(document.querySelector(`[data-value="${value}"]`)).toBeInTheDocument();
    });
    await user.click(document.querySelector(`[data-value="${value}"]`)!);

    expect(mockPush).toHaveBeenCalledWith(href);
    expect(useSearchStore.getState().open).toBe(false);
  });

  it("列出工作区成员，并在选择后跳转到成员页", async () => {
    mockMembers.current = [
      {
        id: "member-1",
        workspace_id: "ws-test",
        user_id: "user-1",
        role: "member",
        created_at: "2026-01-01T00:00:00Z",
        name: "张艾丽",
        account: "alice",
        avatar_url: null,
      },
      {
        id: "member-2",
        workspace_id: "ws-test",
        user_id: "user-2",
        role: "admin",
        created_at: "2026-01-01T00:00:00Z",
        name: "刘博",
        account: "bob",
        avatar_url: null,
      },
    ];
    const user = await renderSearchQuery("alice");

    await waitFor(() => {
      expect(screen.getByText("成员")).toBeInTheDocument();
      expect(
        screen.getByText((_, el) => el?.textContent === "张艾丽" && el?.tagName === "DIV"),
      ).toBeInTheDocument();
    });
    expect(
      screen.getByText((_, el) => el?.textContent === "alice" && el?.tagName === "DIV"),
    ).toBeInTheDocument();
    expect(screen.queryByText("刘博")).not.toBeInTheDocument();

    const aliceItem = await screen.findByText(
      (_, el) => el?.textContent === "张艾丽" && el?.tagName === "DIV",
    );
    await user.click(aliceItem);

    expect(mockPush).toHaveBeenCalledWith("/ws-test/members/user-1");
    expect(useSearchStore.getState().open).toBe(false);
  });

  it("结合查询缓存和访问记录渲染最近访问的 issue", () => {
    mockRecentItems.current = [
      { type: "issue", id: "issue-1", visitedAt: 1000 },
      { type: "issue", id: "issue-2", visitedAt: 900 },
    ];
    mockAllIssues.current = [
      { id: "issue-1", identifier: "MUL-1", title: "第一个 issue", status: "todo" },
      { id: "issue-2", identifier: "MUL-2", title: "第二个 issue", status: "done" },
    ];

    renderSearch();

    expect(screen.getByText("最近")).toBeInTheDocument();
    expect(screen.getByText("第一个 issue")).toBeInTheDocument();
    expect(screen.getByText("MUL-1")).toBeInTheDocument();
    expect(screen.getByText("第二个 issue")).toBeInTheDocument();
    expect(screen.getByText("MUL-2")).toBeInTheDocument();
  });

  it("在命令分组显示新建任务 / 新建项目，并触发 modal store", async () => {
    const user = await renderSearchQuery("new");

    await waitFor(() => {
      expect(screen.getByText("命令")).toBeInTheDocument();
      expect(
        screen.getByText((_, el) => el?.textContent === "新建任务" && el?.tagName === "SPAN"),
      ).toBeInTheDocument();
      expect(
        screen.getByText((_, el) => el?.textContent === "新建项目" && el?.tagName === "SPAN"),
      ).toBeInTheDocument();
    });

    const newIssue = await screen.findByText(
      (_, el) => el?.textContent === "新建任务" && el?.tagName === "SPAN",
    );
    await user.click(newIssue);

    expect(mockOpenModal).toHaveBeenCalledWith("create-issue", null);
    expect(useSearchStore.getState().open).toBe(false);
  });

  it("不在任务详情路由时隐藏复制链接命令", async () => {
    mockPathname.current = "/ws-test/projects";
    await renderSearchQuery("copy");

    // 命令分组可能仍为空或不存在。
    expect(screen.queryByText("复制任务链接")).not.toBeInTheDocument();
  });

  it("在任务详情路由复制任务链接和标识符", async () => {
    // userEvent.setup() installs its own navigator.clipboard; spy on it so we
    // intercept the writeText call without clobbering userEvent's internals.
    const writeSpy = vi
      .spyOn(navigator.clipboard, "writeText")
      .mockImplementation(mockClipboardWrite);
    mockPathname.current = "/ws-test/issues/issue-1";
    mockAllIssues.current = [
      { id: "issue-1", identifier: "MUL-42", title: "演示", status: "todo" },
    ];
    const user = await renderSearchQuery("copy");

    const linkItem = await screen.findByText(
      (_, el) => el?.textContent === "复制任务链接" && el?.tagName === "SPAN",
    );
    await user.click(linkItem);

    expect(mockGetShareableUrl).toHaveBeenCalledWith("/ws-test/issues/issue-1");
    expect(mockClipboardWrite).toHaveBeenCalledWith("https://app.multica//ws-test/issues/issue-1");
    expect(mockToastSuccess).toHaveBeenCalledWith("已复制链接");

    // Reopen palette and test identifier copy
    act(() => {
      useSearchStore.setState({ open: true });
    });
    const input2 = screen.getByPlaceholderText("输入命令或关键词搜索...");
    await user.type(input2, "copy");
    const idItem = await screen.findByText(
      (_, el) =>
        el?.textContent === "复制标识符 (MUL-42)" && el?.tagName === "SPAN",
    );
    await user.click(idItem);
    expect(mockClipboardWrite).toHaveBeenCalledWith("MUL-42");
    expect(mockToastSuccess).toHaveBeenCalledWith("已复制 MUL-42");

    writeSpy.mockRestore();
  });

  it("按查询关键词过滤主题命令", async () => {
    await renderSearchQuery("dark");

    await waitFor(() => {
      expect(screen.getByText("命令")).toBeInTheDocument();
      expect(
        screen.getByText((_, el) => el?.textContent === "切换到深色主题" && el?.tagName === "SPAN"),
      ).toBeInTheDocument();
    });
    expect(screen.queryByText("切换到浅色主题")).not.toBeInTheDocument();
    expect(screen.queryByText("跟随系统主题")).not.toBeInTheDocument();
  });

  it("应用选中的主题并关闭面板", async () => {
    mockTheme.current = "light";
    const user = await renderSearchQuery("dark");

    const darkItem = await screen.findByText(
      (_, el) => el?.textContent === "切换到深色主题" && el?.tagName === "SPAN",
    );
    await user.click(darkItem);

    expect(mockSetTheme).toHaveBeenCalledWith("dark");
    expect(useSearchStore.getState().open).toBe(false);
  });

  it("通过通用 theme 关键词匹配主题操作，并标记当前主题", async () => {
    mockTheme.current = "dark";
    await renderSearchQuery("theme");

    await waitFor(() => {
      expect(
        screen.getByText((_, el) => el?.textContent === "切换到浅色主题" && el?.tagName === "SPAN"),
      ).toBeInTheDocument();
      expect(
        screen.getByText((_, el) => el?.textContent === "切换到深色主题" && el?.tagName === "SPAN"),
      ).toBeInTheDocument();
      expect(
        screen.getByText((_, el) => el?.textContent === "跟随系统主题" && el?.tagName === "SPAN"),
      ).toBeInTheDocument();
    });
    expect(screen.getByLabelText("当前主题")).toBeInTheDocument();
  });

  it("过滤掉查询缓存中不存在的最近访问项", () => {
    mockRecentItems.current = [
      { type: "issue", id: "issue-1", visitedAt: 1000 },
      { type: "issue", id: "deleted-issue", visitedAt: 900 },
    ];
    mockAllIssues.current = [
      { id: "issue-1", identifier: "MUL-1", title: "现有 issue", status: "in_progress" },
    ];

    renderSearch();

    expect(screen.getByText("最近")).toBeInTheDocument();
    expect(screen.getByText("现有 issue")).toBeInTheDocument();
    expect(screen.queryByText("deleted-issue")).not.toBeInTheDocument();
  });

  it("issue 搜索结果显示负责人头像而不是状态文本", async () => {
    mockMembers.current = [
      {
        id: "member-1",
        workspace_id: "ws-test",
        user_id: "user-1",
        role: "member",
        created_at: "2026-01-01T00:00:00Z",
        name: "张艾丽",
        account: "alice",
        avatar_url: null,
      },
    ];
    mockSearchIssues.mockResolvedValue([
        {
          id: "issue-assigned",
          workspace_id: "ws-test",
          number: 101,
          identifier: "MUL-101",
          title: "已分配的搜索结果",
          description: null,
          status: "in_review",
          priority: "none",
          assignee_type: "member",
          assignee_id: "user-1",
          creator_type: "member",
          creator_id: "user-1",
          parent_issue_id: null,
          project_id: null,
          position: 0,
          start_date: null,
          due_date: null,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      ]);

    await renderSearchQuery("assigned");

    await waitFor(
      () => {
        expect(
          screen.getByText((_, el) =>
            el?.textContent === "已分配的搜索结果" &&
            el?.tagName === "SPAN",
          ),
        ).toBeInTheDocument();
      },
      { timeout: 2000 },
    );

    expect(screen.getByTitle("张艾丽")).toBeInTheDocument();
    expect(screen.queryByText("审核中")).not.toBeInTheDocument();
  });

  it("搜索接口失败时清空旧结果并显示可诊断错误", async () => {
    mockSearchIssues.mockRejectedValue(new Error("service unavailable"));
    await renderSearchQuery("unavailable");

    expect(
      await screen.findByRole("alert", undefined, { timeout: 2000 }),
    ).toHaveTextContent("搜索服务暂时不可用，请稍后重试。");
    expect(screen.queryByText("未找到结果。")).not.toBeInTheDocument();
  });

  it("最近访问 issue 显示负责人头像而不是状态文本", () => {
    mockRecentItems.current = [
      { type: "issue", id: "issue-1", visitedAt: 1000 },
    ];
    mockAgents.current = [{ id: "agent-1", name: "Niko", avatar_url: null }];
    mockAllIssues.current = [
      {
        id: "issue-1",
        identifier: "MUL-1",
        title: "最近分配的 issue",
        status: "done",
        assignee_type: "agent",
        assignee_id: "agent-1",
      },
    ];

    renderSearch();

    expect(screen.getByText("最近分配的 issue")).toBeInTheDocument();
    expect(screen.getByTitle("Niko")).toBeInTheDocument();
    expect(screen.queryByText("已完成")).not.toBeInTheDocument();
  });

  it("同时渲染描述和评论匹配片段", async () => {
    mockSearchIssues.mockResolvedValue([
        {
          id: "issue-snippet",
          workspace_id: "ws-test",
          number: 99,
          identifier: "MUL-99",
          title: "HTML 渲染流水线",
          description: null,
          status: "todo",
          priority: "none",
          assignee_type: null,
          assignee_id: null,
          creator_type: "member",
          creator_id: "user-1",
          parent_issue_id: null,
          project_id: null,
          position: 0,
          start_date: null,
          due_date: null,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
          matched_description_snippet: "...uses HTML templates for rendering...",
          matched_comment_snippet: "...we should migrate away from HTML...",
        },
      ]);
    await renderSearchQuery("html");

    await waitFor(
      () => {
        expect(screen.getByText((_, el) => el?.textContent === "HTML 渲染流水线" && el?.tagName === "SPAN")).toBeInTheDocument();
      },
      { timeout: 2000 },
    );

    // Both independent snippets remain visible when the backend returns them.
    expect(
      screen.getByText((_, el) =>
        (el?.textContent?.includes("uses HTML templates for rendering") ?? false) &&
        el?.tagName === "SPAN",
      ),
    ).toBeInTheDocument();

    expect(
      screen.getByText((_, el) =>
        (el?.textContent?.includes("we should migrate away from HTML") ?? false) &&
        el?.tagName === "SPAN",
      ),
    ).toBeInTheDocument();
  });
});
