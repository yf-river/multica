import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { SupportedLocale } from "@multica/core/i18n";
import zhOnboarding from "../locales/zh-Hans/onboarding.json";
import zhCommon from "../locales/zh-Hans/common.json";
import { NavigationProvider } from "../navigation";
import type { NavigationAdapter } from "../navigation";
import { useWelcomeStore } from "@multica/core/onboarding";
import { WelcomeAfterOnboarding } from "./welcome-after-onboarding";

const TEST_RESOURCES = {
  "zh-Hans": { common: zhCommon, onboarding: zhOnboarding },
};

// `useAuthStore` is a singleton Proxy that requires `registerAuthStore`
// to be called before use. In tests we mock the module wholesale so the
// component reads a fixed user without ever touching the proxy.
const mockUser = {
  id: "user-1",
  name: "Test",
  account: "test",
  avatar_url: null,
  onboarded_at: "2026-01-01T00:00:00Z",
  onboarding_questionnaire: {},
  starter_content_state: null,
  profile_description: "",
  created_at: "",
  updated_at: "",
};
vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: (s: { user: typeof mockUser }) => unknown) => {
      const state = { user: mockUser };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ user: mockUser }) },
  ),
  registerAuthStore: vi.fn(),
  createAuthStore: vi.fn(),
}));

const mockListAgents = vi.fn();
const mockCreateAgent = vi.fn();
const mockCreateIssue = vi.fn();
const mockCreateComment = vi.fn();
const mockGetWorkspace = vi.fn();

// `useCurrentWorkspace` is gated by `WorkspaceSlugProvider`; in tests
// we short-circuit to a fixture matching the welcome signal's workspace id
// so the cross-workspace guard doesn't drop the component.
vi.mock("@multica/core/paths", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/paths")>(
    "@multica/core/paths",
  );
  return {
    ...actual,
    useCurrentWorkspace: () => ({
      id: "ws-1",
      slug: "test-ws",
      name: "Test WS",
    }),
  };
});

vi.mock("@multica/core/api", () => ({
  api: {
    getBaseUrl: () => "http://127.0.0.1:8080",
    listAgents: (...args: unknown[]) => mockListAgents(...args),
    createAgent: (...args: unknown[]) => mockCreateAgent(...args),
    createIssue: (...args: unknown[]) => mockCreateIssue(...args),
    createComment: (...args: unknown[]) => mockCreateComment(...args),
    getWorkspace: (...args: unknown[]) => mockGetWorkspace(...args),
  },
}));

const mockPush = vi.fn();
const navigationAdapter: NavigationAdapter = {
  push: (path: string) => mockPush(path),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/test",
  searchParams: new URLSearchParams(),
  getShareableUrl: (path: string) => `https://test.local${path}`,
};

function I18nWrapper({
  children,
  locale = "zh-Hans",
}: {
  children: ReactNode;
  locale?: SupportedLocale;
}) {
  return (
    <I18nProvider locale={locale} resources={TEST_RESOURCES}>
      <NavigationProvider value={navigationAdapter}>
        {children}
      </NavigationProvider>
    </I18nProvider>
  );
}

function renderWelcome({ locale = "zh-Hans" }: { locale?: SupportedLocale } = {}) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  qc.setQueryData(["workspaces", "list"], [{ id: "ws-1", slug: "test-ws" }]);
  return render(<WelcomeAfterOnboarding />, {
    wrapper: ({ children }) => (
      <QueryClientProvider client={qc}>
        <I18nWrapper locale={locale}>{children}</I18nWrapper>
      </QueryClientProvider>
    ),
  });
}

beforeEach(() => {
  mockListAgents.mockReset();
  mockCreateAgent.mockReset();
  mockCreateIssue.mockReset();
  mockCreateComment.mockReset();
  mockGetWorkspace.mockReset();
  mockPush.mockReset();
  useWelcomeStore.getState().reset();
});

describe("WelcomeAfterOnboarding", () => {
  it("renders nothing when no welcome signal is present", () => {
    const { container } = renderWelcome();
    expect(container.firstChild).toBeNull();
  });

  it("renders nothing when the signal points at a different workspace", () => {
    // Cross-workspace guard: store may have a signal parked from
    // workspace ws-2 while the user is currently viewing ws-1 (the
    // mocked useCurrentWorkspace returns ws-1). Don't fire here —
    // otherwise we'd create the Helper / seed issues in ws-2 while the
    // user looks at ws-1, then navigate them away unexpectedly.
    useWelcomeStore.getState().set({
      workspaceId: "ws-2",
      choice: "skip",
    });
    const { container } = renderWelcome();
    expect(container.firstChild).toBeNull();
    expect(mockCreateIssue).not.toHaveBeenCalled();
  });

  describe("runtime path", () => {
    it("creates a Helper agent then opens a blocking modal with starter cards", async () => {
      mockListAgents.mockResolvedValueOnce([]);
      mockCreateAgent.mockResolvedValueOnce({
        id: "agent-1",
        name: "Multica Helper",
        description: "Built-in workspace assistant.",
        avatar_url: null,
        scope: "workspace",
      });
      useWelcomeStore.getState().set({
        workspaceId: "ws-1",
        choice: "runtime",
        runtimeId: "rt-1",
      });

      renderWelcome();

      expect(screen.getByText(/正在为你准备 Helper/)).toBeInTheDocument();

      await waitFor(() => {
        expect(screen.getByText(/欢迎来到 Multica/)).toBeInTheDocument();
      });

      expect(mockCreateAgent).toHaveBeenCalledTimes(1);
      const [agentArgs] = mockCreateAgent.mock.calls[0]!;
      expect(agentArgs.runtime_id).toBe("rt-1");
      expect(agentArgs.name).toBe("Multica Helper");
      expect(agentArgs.max_concurrent_tasks).toBeUndefined();
      expect(agentArgs.instructions).toContain("Multica Helper");

      // 3 starter card titles come from Chinese-only persisted constants.
      expect(screen.getByText("简单介绍一下 Multica")).toBeInTheDocument();
      expect(screen.getByText("带我熟悉每个功能")).toBeInTheDocument();
      expect(
        screen.getByText("用 slides 介绍 Multica 能为我做什么"),
      ).toBeInTheDocument();
    });

    it("reuses an existing Multica Helper agent instead of creating duplicates", async () => {
      mockListAgents.mockResolvedValueOnce([
        {
          id: "agent-existing",
          name: "Multica Helper",
          description: "",
          avatar_url: null,
          scope: "workspace",
          archived_at: null,
        },
      ]);
      useWelcomeStore.getState().set({
        workspaceId: "ws-1",
        choice: "runtime",
        runtimeId: "rt-1",
      });

      renderWelcome();
      await waitFor(() => {
        expect(screen.getByText(/欢迎来到 Multica/)).toBeInTheDocument();
      });

      expect(mockCreateAgent).not.toHaveBeenCalled();
    });

    it("selecting cards then clicking Assign creates one issue per pick and navigates to the first", async () => {
      mockListAgents.mockResolvedValueOnce([]);
      mockCreateAgent.mockResolvedValueOnce({
        id: "agent-1",
        name: "Multica Helper",
        description: "",
        avatar_url: null,
        scope: "workspace",
      });
      // Pick 2 cards — `intro` then `welcome_page`. Issues come back in
      // STARTER_CARD_IDS order (intro first), so navigate target is the
      // intro issue.
      mockCreateIssue
        .mockResolvedValueOnce({
          id: "issue-intro",
          workspace_id: "ws-1",
        })
        .mockResolvedValueOnce({
          id: "issue-welcome",
          workspace_id: "ws-1",
        });
      useWelcomeStore.getState().set({
        workspaceId: "ws-1",
        choice: "runtime",
        runtimeId: "rt-1",
      });

      renderWelcome();
      await waitFor(() =>
        expect(screen.getByText("简单介绍一下 Multica")).toBeInTheDocument(),
      );

      // CTA is disabled until at least one card is selected.
      const ctaEmpty = screen.getByRole("button", { name: /选一个或多个/ });
      expect(ctaEmpty).toBeDisabled();

      // Toggle two cards.
      fireEvent.click(screen.getByText("简单介绍一下 Multica"));
      fireEvent.click(
        screen.getByText("用 slides 介绍 Multica 能为我做什么"),
      );

      // CTA enables and reflects the count.
      const cta = await screen.findByRole("button", { name: /分配 2 个任务给我/ });
      expect(cta).not.toBeDisabled();
      fireEvent.click(cta);

      await waitFor(() => expect(mockCreateIssue).toHaveBeenCalledTimes(2));
      const titles = mockCreateIssue.mock.calls.map(([args]) => args.title);
      expect(titles).toEqual([
        "简单介绍一下 Multica",
        "用 slides 介绍 Multica 能为我做什么",
      ]);
      // Both assigned to the same Helper agent.
      mockCreateIssue.mock.calls.forEach(([args]) => {
        expect(args.assignee_type).toBe("agent");
        expect(args.assignee_id).toBe("agent-1");
      });

      // After Promise.all resolves we DO NOT navigate immediately — the
      // Modal switches to a success view. The user must click the confirm
      // button on that view to navigate.
      const gotIt = await screen.findByRole("button", { name: /知道了/ });
      expect(mockPush).not.toHaveBeenCalled();
      fireEvent.click(gotIt);

      // Navigates to the first issue (intro, since it's earlier in
      // STARTER_CARD_IDS).
      await waitFor(() =>
        expect(mockPush).toHaveBeenCalledWith("/test-ws/issues/issue-intro"),
      );
    });

  });

  describe("skip path", () => {
    it("provisions install-runtime → agent-guide → follow-up comment, then opens the celebration Modal", async () => {
      // Sequential provisioning order in the implementation:
      //   1. install-runtime (Step 1, body is static, becomes the mention
      //      chip target inside agent-guide's body)
      //   2. agent-guide (Step 2, body embeds install-runtime mention)
      //   3. comment on install-runtime (mentions agent-guide back)
      mockCreateIssue
        .mockResolvedValueOnce({
          id: "issue-install",
          identifier: "MUL-1",
          workspace_id: "ws-1",
        })
        .mockResolvedValueOnce({
          id: "issue-agent",
          identifier: "MUL-2",
          workspace_id: "ws-1",
        });
      mockCreateComment.mockResolvedValueOnce({ id: "comment-1" });

      useWelcomeStore.getState().set({
        workspaceId: "ws-1",
        choice: "skip",
      });

      renderWelcome();

      // Loading veil shows first.
      expect(screen.getByText(/正在为你准备工作区/)).toBeInTheDocument();

      // Modal appears once all 3 API calls succeed.
      await waitFor(() => {
        expect(screen.getByText(/欢迎来到 Multica/)).toBeInTheDocument();
      });

      expect(mockCreateIssue).toHaveBeenCalledTimes(2);
      expect(mockCreateComment).toHaveBeenCalledTimes(1);

      // First createIssue call: install-runtime (Step 1).
      const [firstCall] = mockCreateIssue.mock.calls;
      expect(firstCall![0].title).toBe(
        "第 1 步 —— 连接运行时,开始使用 agent",
      );
      expect(firstCall![0].status).toBe("in_progress");
      expect(firstCall![0].assignee_type).toBe("member");
      expect(firstCall![0].assignee_id).toBe("user-1");

      // Second createIssue call: agent-guide (Step 2), body must embed
      // install-runtime mention chip pointing at MUL-1 / issue-install.
      const [secondCall] = mockCreateIssue.mock.calls.slice(1);
      expect(secondCall![0].title).toBe(
        "第 2 步 —— 创建你的第一个 Multica Agent",
      );
      expect(secondCall![0].status).toBe("todo");
      expect(secondCall![0].description).toContain(
        "[MUL-1](mention://issue/issue-install)",
      );

      // Follow-up comment posted on install-runtime as a mention chip
      // pointing at the agent-guide issue.
      const [commentIssueId, commentContent] =
        mockCreateComment.mock.calls[0]!;
      expect(commentIssueId).toBe("issue-install");
      expect(commentContent).toContain("[MUL-2](mention://issue/issue-agent)");
    });

    it("silently dismisses without showing the Modal when provisioning fails", async () => {
      mockCreateIssue.mockRejectedValueOnce(new Error("network down"));
      useWelcomeStore.getState().set({
        workspaceId: "ws-1",
        choice: "skip",
      });

      renderWelcome();

      // Failure path: loading veil shows, then unmounts as the store is
      // dismissed. No celebration Modal ever appears.
      await waitFor(() =>
        expect(useWelcomeStore.getState().dismissed).toBe(true),
      );
      expect(screen.queryByText(/欢迎来到 Multica/)).not.toBeInTheDocument();
    });
  });
});
