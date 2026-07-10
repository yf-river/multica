import { forwardRef, useRef, useState, useImperativeHandle } from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Issue, TimelineEntry } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enIssues from "../../locales/zh-Hans/issues.json";

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, issues: enIssues } };

const mockViewport = vi.hoisted(() => ({ isMobile: false }));

vi.mock("@multica/ui/hooks/use-mobile", () => ({
  useIsMobile: () => mockViewport.isMobile,
}));

// useWorkspaceId() derives from useCurrentWorkspace (relative import inside
// @multica/core/hooks.tsx). vi.mock("@multica/core/paths") only intercepts
// the bare-specifier, not the internal relative import. Mock the hooks module
// directly so the bridge hook returns the test UUID.
vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

// Mock @multica/core/auth
const mockAuthUser = { id: "user-1", account: "test", name: "Test User" };
vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: any) => {
      const state = { user: mockAuthUser, isAuthenticated: true };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ user: mockAuthUser, isAuthenticated: true }) },
  ),
  registerAuthStore: vi.fn(),
  createAuthStore: vi.fn(),
}));

// Mock @multica/core/workspace/hooks
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getMemberName: (id: string) => (id === "user-1" ? "Test User" : "Unknown"),
    getAgentName: (id: string) => (id === "agent-1" ? "Claude Agent" : "Unknown Agent"),
    getActorName: (type: string, id: string) => {
      if (type === "member" && id === "user-1") return "Test User";
      if (type === "agent" && id === "agent-1") return "Claude Agent";
      return "Unknown";
    },
    getActorInitials: (type: string) => (type === "member" ? "TU" : "CA"),
    getActorAvatarUrl: () => null,
  }),
}));

// Mock workspace queries
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({
    queryKey: ["workspaces", "ws-1", "members"],
    queryFn: () => Promise.resolve([{ user_id: "user-1", name: "Test User", account: "test", role: "admin" }]),
  }),
  agentListOptions: () => ({
    queryKey: ["workspaces", "ws-1", "agents"],
    queryFn: () => Promise.resolve([]),
  }),
  squadListOptions: () => ({
    queryKey: ["workspaces", "ws-1", "squads"],
    queryFn: () => Promise.resolve([]),
  }),
  assigneeFrequencyOptions: () => ({
    queryKey: ["workspaces", "ws-1", "assignee-frequency"],
    queryFn: () => Promise.resolve([]),
  }),
  workspaceListOptions: () => ({
    queryKey: ["workspaces"],
    queryFn: () => Promise.resolve([{ id: "ws-1", name: "Test WS", slug: "test" }]),
  }),
}));

// Mock @multica/core/paths — after the URL-driven workspace refactor,
// useCurrentWorkspace / useWorkspacePaths derive from the workspace slug in
// URL Context. Tests don't mount a real route, so we short-circuit to fixtures.
vi.mock("@multica/core/paths", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/paths")>(
    "@multica/core/paths",
  );
  return {
    ...actual,
    useCurrentWorkspace: () => ({ id: "ws-1", name: "Test WS", slug: "test" }),
    useWorkspacePaths: () => actual.paths.workspace("test"),
  };
});

// Mock navigation
vi.mock("../../navigation", () => ({
  AppLink: ({ children, href, ...props }: any) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
  useNavigation: () => ({
    push: vi.fn(),
    pathname: "/issues/issue-1",
    getShareableUrl: (p: string) => `https://app.multica.com${p}`,
  }),
  NavigationProvider: ({ children }: { children: React.ReactNode }) => children,
}));

// Mock editor components (Tiptap requires real DOM)
vi.mock("../../editor", () => ({
  useFileDropZone: () => ({ isDragOver: false, dropZoneProps: {} }),
  FileDropOverlay: () => null,
  // No-op so comment-card's AttachmentList can render without hitting the
  // real API singleton; tests that care about download wiring should write
  // dedicated specs against `use-download-attachment.test.tsx`.
  useDownloadAttachment: () => vi.fn(),
  // Inert preview hook — comment-card's AttachmentList uses it to gate the
  // Eye button. Dedicated coverage lives in attachment-preview-modal.test.tsx.
  useAttachmentPreview: () => ({
    open: vi.fn(),
    tryOpen: () => false,
    modal: null,
  }),
  isPreviewable: () => false,
  ReadonlyContent: ({ content }: { content: string }) => (
    <div data-testid="readonly-content">{content}</div>
  ),
  ContentEditor: forwardRef(function MockContentEditor(
    { defaultValue, onUpdate, placeholder, flushPendingOnUnmount }: any,
    ref: any,
  ) {
    const valueRef = useRef(defaultValue || "");
    const [value, setValue] = useState(defaultValue || "");
    useImperativeHandle(ref, () => ({
      getMarkdown: () => valueRef.current,
      clearContent: () => { valueRef.current = ""; setValue(""); },
      focus: () => {},
      uploadFile: () => {},
    }));
    return (
      <textarea
        value={value}
        onChange={(e) => {
          valueRef.current = e.target.value;
          setValue(e.target.value);
          onUpdate?.(e.target.value);
        }}
        placeholder={placeholder}
        data-testid="rich-text-editor"
        data-flush-on-unmount={flushPendingOnUnmount ? "true" : undefined}
      />
    );
  }),
  TitleEditor: forwardRef(function MockTitleEditor(
    { defaultValue, placeholder, onBlur, onChange }: any,
    ref: any,
  ) {
    const valueRef = useRef(defaultValue || "");
    const [value, setValue] = useState(defaultValue || "");
    useImperativeHandle(ref, () => ({
      getText: () => valueRef.current,
      focus: () => {},
    }));
    return (
      <input
        value={value}
        onChange={(e) => {
          valueRef.current = e.target.value;
          setValue(e.target.value);
          onChange?.(e.target.value);
        }}
        onBlur={() => onBlur?.(valueRef.current)}
        placeholder={placeholder}
        data-testid="title-editor"
      />
    );
  }),
}));

// Mock common components
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorType, actorId }: any) => (
    <span data-testid="actor-avatar">
      {actorType}:{actorId}
    </span>
  ),
}));

vi.mock("../../projects/components/project-picker", () => ({
  ProjectPicker: () => <span data-testid="project-picker">Project</span>,
}));

// Mock api
const mockApiObj = vi.hoisted(() => ({
  getIssue: vi.fn(),
  listTimeline: vi.fn().mockResolvedValue([]),
  createComment: vi.fn(),
  updateComment: vi.fn(),
  deleteComment: vi.fn(),
  deleteIssue: vi.fn(),
  updateIssue: vi.fn(),
  listIssueSubscribers: vi.fn().mockResolvedValue([]),
  subscribeToIssue: vi.fn().mockResolvedValue(undefined),
  unsubscribeFromIssue: vi.fn().mockResolvedValue(undefined),
  listTasksByIssue: vi.fn().mockResolvedValue([]),
  listIssueTaskTraceEvents: vi.fn().mockResolvedValue({ events: [] }),
  getIssueExecutionTree: vi.fn().mockResolvedValue(null),
  listIssueSOPRuns: vi.fn().mockResolvedValue({ items: [] }),
  rerunIssue: vi.fn(),
  listTaskMessages: vi.fn().mockResolvedValue([]),
  listChildIssues: vi.fn().mockResolvedValue({ issues: [] }),
  listIssues: vi.fn().mockResolvedValue({ issues: [], total: 0 }),
  uploadFile: vi.fn(),
  listIssueReactions: vi.fn().mockResolvedValue([]),
  addIssueReaction: vi.fn(),
  removeIssueReaction: vi.fn(),
  listAttachments: vi.fn().mockResolvedValue([]),
  addCommentReaction: vi.fn(),
  removeCommentReaction: vi.fn(),
  listMembers: vi.fn().mockResolvedValue([{ user_id: "user-1", name: "Test User", account: "test", role: "admin" }]),
  listAgents: vi.fn().mockResolvedValue([]),
  getProject: vi.fn(),
  listProjects: vi.fn().mockResolvedValue({ projects: [] }),
}));

vi.mock("@multica/core/api", () => ({
  api: mockApiObj,
  getApi: () => mockApiObj,
  setApiInstance: vi.fn(),
}));

// Mock issue config
vi.mock("@multica/core/issues/config", () => ({
  ALL_STATUSES: ["backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled"],
  BOARD_STATUSES: ["backlog", "todo", "in_progress", "in_review", "done", "blocked"],
  STATUS_ORDER: ["backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled"],
  STATUS_CONFIG: {
    backlog: { label: "待办池", iconColor: "text-muted-foreground", hoverBg: "hover:bg-accent" },
    todo: { label: "待处理", iconColor: "text-muted-foreground", hoverBg: "hover:bg-accent" },
    in_progress: { label: "进行中", iconColor: "text-warning", hoverBg: "hover:bg-warning/10" },
    in_review: { label: "评审中", iconColor: "text-success", hoverBg: "hover:bg-success/10" },
    done: { label: "已完成", iconColor: "text-info", hoverBg: "hover:bg-info/10" },
    blocked: { label: "已阻塞", iconColor: "text-destructive", hoverBg: "hover:bg-destructive/10" },
    cancelled: { label: "已取消", iconColor: "text-muted-foreground", hoverBg: "hover:bg-accent" },
  },
  PRIORITY_ORDER: ["urgent", "high", "medium", "low", "none"],
  PRIORITY_CONFIG: {
    urgent: { label: "Urgent", bars: 4, color: "text-destructive", badgeBg: "bg-destructive/10", badgeText: "text-destructive" },
    high: { label: "High", bars: 3, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
    medium: { label: "Medium", bars: 2, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
    low: { label: "Low", bars: 1, color: "text-info", badgeBg: "bg-info/10", badgeText: "text-info" },
    none: { label: "No priority", bars: 0, color: "text-muted-foreground", badgeBg: "bg-muted", badgeText: "text-muted-foreground" },
  },
}));

// Mock recent issues store
const mockRecordVisit = vi.fn();
vi.mock("@multica/core/issues/stores", () => ({
  useRecentIssuesStore: Object.assign(
    (selector?: any) => {
      const state = { byWorkspace: {}, recordVisit: mockRecordVisit, pruneWorkspaces: vi.fn() };
      return selector ? selector(state) : state;
    },
    {
      getState: () => ({
        byWorkspace: {},
        recordVisit: mockRecordVisit,
        pruneWorkspaces: vi.fn(),
      }),
    },
  ),
  selectRecentIssues: () => () => [],
  useCommentCollapseStore: (selector?: any) => {
    const state = {
      collapsedByIssue: {},
      isCollapsed: () => false,
      toggle: () => {},
    };
    return selector ? selector(state) : state;
  },
  useCommentDraftStore: Object.assign(
    (selector?: any) => {
      const state = {
        drafts: {} as Record<string, { content: string; updatedAt: number }>,
        getDraft: () => undefined,
        setDraft: () => {},
        clearDraft: () => {},
      };
      return selector ? selector(state) : state;
    },
    {
      getState: () => ({
        drafts: {} as Record<string, { content: string; updatedAt: number }>,
        getDraft: () => undefined,
        setDraft: () => {},
        clearDraft: () => {},
      }),
    },
  ),
}));

// Mock react-virtuoso: jsdom has no real layout, so the real Virtuoso would
// compute a 0-height viewport and render nothing. The mock renders every item
// inline so id="comment-..." nodes are always present in the DOM — this
// matches the production cold-path where `initialItemCount` force-mounts
// items[0..targetIdx], giving the deep-link effect a real target node.
//
// scrollIntoViewSpy: the deep-link effect no longer calls native
// scrollIntoView (it drives the timeline container's scrollTop directly to
// avoid scrolling ancestor overflow:hidden boxes — see issue-detail.tsx). We
// keep a no-op stub on the prototype so any stray scrollIntoView call from
// other components doesn't throw; deep-link tests assert the highlight ring
// instead, which is mechanism-independent and observable without layout.
const scrollIntoViewSpy = vi.hoisted(() => vi.fn());

vi.mock("react-virtuoso", () => ({
  Virtuoso: forwardRef(function MockVirtuoso(
    { data, itemContent }: { data: unknown[]; itemContent: (i: number, item: unknown) => unknown },
    ref: any,
  ) {
    useImperativeHandle(ref, () => ({
      // Real Virtuoso ref methods are not exercised by tests in this file
      // since the deep-link cold-path drives the container's scrollTop on the
      // real DOM node, not Virtuoso's imperative API.
      scrollIntoView: vi.fn(),
      scrollToIndex: vi.fn(),
    }));
    return (
      <div data-testid="virtuoso-mock">
        {data.map((item, i) => (
          <div key={i}>{itemContent(i, item) as React.ReactElement}</div>
        ))}
      </div>
    );
  }),
}));

// jsdom's HTMLElement.prototype.scrollIntoView is a no-op stub; replace it
// with a spy so the deep-link effect's call can be observed.
beforeEach(() => {
  scrollIntoViewSpy.mockClear();
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
    configurable: true,
    writable: true,
    value: scrollIntoViewSpy,
  });
});

// Mock modals
vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(
    () => ({ open: vi.fn() }),
    { getState: () => ({ open: vi.fn() }) },
  ),
}));

// Mock core/hooks/use-file-upload
vi.mock("@multica/core/hooks/use-file-upload", () => ({
  useFileUpload: () => ({ uploadWithToast: vi.fn().mockResolvedValue("https://example.com/file.png") }),
}));

// Mock realtime
vi.mock("@multica/core/realtime", () => ({
  useWSEvent: vi.fn(),
  useWSReconnect: vi.fn(),
  useWS: () => ({ subscribe: vi.fn(() => () => {}), onReconnect: vi.fn(() => () => {}) }),
  WSProvider: ({ children }: { children: React.ReactNode }) => children,
  useRealtimeSync: () => {},
}));

// Mock sonner
vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

// Mock react-resizable-panels (used by @multica/ui/components/ui/resizable)
vi.mock("react-resizable-panels", () => ({
  Group: ({ children, ...props }: any) => <div data-testid="panel-group" {...props}>{children}</div>,
  Panel: ({ children, ...props }: any) => <div data-testid="panel" {...props}>{children}</div>,
  Separator: ({ children, ...props }: any) => <div data-testid="panel-handle" {...props}>{children}</div>,
  useDefaultLayout: () => ({ defaultLayout: undefined, onLayoutChanged: vi.fn() }),
  usePanelRef: () => ({ current: { isCollapsed: () => false, expand: vi.fn(), collapse: vi.fn() } }),
}));

// ---------------------------------------------------------------------------
// Test data
// ---------------------------------------------------------------------------

const mockIssue: Issue = {
  id: "issue-1",
  workspace_id: "ws-1",
  number: 1,
  identifier: "TES-1",
  title: "Implement authentication",
  description: "Add JWT auth to the backend",
  status: "in_progress",
  priority: "high",
  assignee_type: "member",
  assignee_id: "user-1",
  creator_type: "member",
  creator_id: "user-1",
  parent_issue_id: null,
  project_id: null,
  position: 0,
  start_date: null,
  due_date: "2026-06-01T00:00:00Z",
  metadata: {},
  created_at: "2026-01-15T00:00:00Z",
  updated_at: "2026-01-20T00:00:00Z",
};

const mockTimeline: TimelineEntry[] = [
  {
    type: "comment",
    id: "comment-1",
    actor_type: "member",
    actor_id: "user-1",
    content: "Started working on this",
    parent_id: null,
    created_at: "2026-01-16T00:00:00Z",
    updated_at: "2026-01-16T00:00:00Z",
    comment_type: "comment",
  },
  {
    type: "comment",
    id: "comment-2",
    actor_type: "agent",
    actor_id: "agent-1",
    content: "I can help with this",
    parent_id: null,
    created_at: "2026-01-17T00:00:00Z",
    updated_at: "2026-01-17T00:00:00Z",
    comment_type: "comment",
  },
];

// ---------------------------------------------------------------------------
// Import component under test (after mocks)
// ---------------------------------------------------------------------------

import { IssueDetail } from "./issue-detail";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function createTestQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  });
}

function renderIssueDetail(issueId = "issue-1") {
  const queryClient = createTestQueryClient();
  return render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <IssueDetail issueId={issueId} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

function renderIssueDetailWithHighlight(
  highlightCommentId: string,
  issueId = "issue-1",
  options: { seedTimeline?: boolean } = {},
) {
  const queryClient = createTestQueryClient();
  if (options.seedTimeline) {
    // Pre-populate the timeline cache so the first render sees timeline.length>0.
    // This reproduces the inbox-click race: timeline data is available before
    // the issue itself has finished loading, so the effect that scrolls to
    // the comment fires once with `loading=true` (skeleton still rendered,
    // no comment DOM) and must re-fire when `loading` flips to false.
    queryClient.setQueryData(["issues", "timeline", issueId], mockTimeline);
  }
  const result = render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <IssueDetail issueId={issueId} highlightCommentId={highlightCommentId} />
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { ...result, queryClient };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("IssueDetail (shared)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockViewport.isMobile = false;
    // Default: issue loads successfully
    mockApiObj.getIssue.mockResolvedValue(mockIssue);
    // /timeline returns the entries flat in chronological order (oldest first).
    mockApiObj.listTimeline.mockResolvedValue(mockTimeline);
    mockApiObj.listIssueReactions.mockResolvedValue([]);
    mockApiObj.listIssueSubscribers.mockResolvedValue([]);
    mockApiObj.listChildIssues.mockResolvedValue({ issues: [] });
    mockApiObj.listIssues.mockResolvedValue({ issues: [], total: 0 });
    mockApiObj.listTasksByIssue.mockResolvedValue([]);
    mockApiObj.listIssueTaskTraceEvents.mockResolvedValue({ events: [] });
    mockApiObj.getIssueExecutionTree.mockResolvedValue(null);
    mockApiObj.listIssueSOPRuns.mockResolvedValue({ items: [] });
    mockApiObj.rerunIssue.mockResolvedValue({ id: "task-rerun" });
    mockApiObj.listMembers.mockResolvedValue([
      { user_id: "user-1", name: "Test User", account: "test", role: "admin" },
    ]);
    mockApiObj.listAgents.mockResolvedValue([]);
    // Reset project mock — individual tests override per case. Default fixture
    // has project_id: null so getProject is not invoked.
    mockApiObj.getProject.mockReset();
  });

  it("shows loading skeleton while data is loading", () => {
    // Make the API hang to keep loading state
    mockApiObj.getIssue.mockReturnValue(new Promise(() => {}));
    renderIssueDetail();

    expect(
      screen.getAllByRole("generic").some((el) => el.getAttribute("data-slot") === "skeleton"),
    ).toBe(true);
  });

  it("renders issue title and description after loading", async () => {
    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByDisplayValue("Implement authentication")).toBeInTheDocument();
    });

    expect(screen.getByDisplayValue("Add JWT auth to the backend")).toBeInTheDocument();
  });

  it("shows a loading indicator instead of the TAPD source summary placeholder", async () => {
    mockApiObj.getIssue.mockResolvedValue({
      ...mockIssue,
      description: "## 需求摘要\n摘要生成中，系统正在基于 TAPD 来源生成可执行的需求摘要。",
      metadata: {
        source_provider: "tapd",
        source_url: "https://www.tapd.cn/51081496/prong/stories/view/1151081496001028216",
        tapd_resource_type: "story",
        tapd_resource_id: "1151081496001028216",
        source_summary_status: "pending",
      },
    });

    renderIssueDetail();

    expect(await screen.findByTestId("source-summary-loading")).toHaveTextContent("正在生成需求摘要");
    expect(screen.queryByDisplayValue(/摘要生成中，系统正在基于 TAPD 来源生成可执行的需求摘要/)).not.toBeInTheDocument();
    expect(screen.queryByText(/摘要生成中，系统正在基于 TAPD 来源生成可执行的需求摘要/)).not.toBeInTheDocument();
  });

  it("opts the description editor into the unmount flush", async () => {
    // Closing the issue modal must save the description the user last saw —
    // ContentEditor drops pending debounced updates on unmount by default
    // (so cancelled comment drafts aren't resurrected), and only this
    // explicit opt-in keeps a paste-then-close from losing the image
    // markdown and its attachment_ids bind (MUL-3254). The flush behavior
    // itself is covered in content-editor.test.tsx; this pins the wiring.
    renderIssueDetail();

    const description = await screen.findByDisplayValue("Add JWT auth to the backend");
    expect(description).toHaveAttribute("data-flush-on-unmount", "true");
  });

  it("renders the issue title leaf as a link to the issue detail page", async () => {
    renderIssueDetail();

    // The breadcrumb leaf is the whole "identifier + title" string wrapped in a
    // single link to the issue's own detail route (used to open the full page
    // from the inline Inbox pane). A bare issue has no ancestor crumbs.
    const leaf = await screen.findByText("TES-1 Implement authentication");
    expect(leaf.closest("a")).toHaveAttribute("href", "/test/issues/issue-1");
  });

  it("omits the project breadcrumb segment when the issue has no project_id", async () => {
    // Default fixture has project_id: null.
    renderIssueDetail();

    // Leaf renders once loaded; a bare issue has no ancestor crumbs at all.
    await screen.findByText("TES-1 Implement authentication");

    // Project is never fetched and no project crumb appears.
    expect(mockApiObj.getProject).not.toHaveBeenCalled();
    expect(screen.queryByText("Marketing site refresh")).not.toBeInTheDocument();
  });

  it("renders the project breadcrumb segment when the issue belongs to a project", async () => {
    mockApiObj.getIssue.mockResolvedValue({ ...mockIssue, project_id: "p-1" });
    mockApiObj.getProject.mockResolvedValue({
      id: "p-1",
      workspace_id: "ws-1",
      title: "Marketing site refresh",
      description: null,
      icon: "🚀",
      status: "in_progress",
      priority: "none",
      lead_type: null,
      lead_id: null,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      issue_count: 0,
      done_count: 0,
      resource_count: 0,
    });

    renderIssueDetail();

    const projectLink = await screen.findByText("Marketing site refresh");
    // The whole project segment is a single AppLink pointing at the project
    // detail route under the active workspace slug.
    expect(projectLink.closest("a")).toHaveAttribute("href", "/test/projects/p-1");
  });

  it("renders properties sidebar with all core rows plus set optional rows", async () => {
    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByText("属性")).toBeInTheDocument();
    });

    // Core rows — always rendered regardless of whether the issue has a value.
    expect(screen.getByText("状态")).toBeInTheDocument();
    expect(screen.getByText("负责人")).toBeInTheDocument();
    // "Project" appears twice (row label + picker stub), so disambiguate by id.
    expect(screen.getByTestId("project-picker")).toBeInTheDocument();
    // priority="high" + due_date are set in the fixture, so both optional rows show.
    expect(screen.getByText("优先级")).toBeInTheDocument();
    expect(screen.getByText("截止日期")).toBeInTheDocument();
    // No labels are attached in the fixture — the Labels optional row
    // must stay hidden by default.
    expect(screen.queryByText("标签")).not.toBeInTheDocument();
    // Parent issue lives in its own section and only renders when the
    // issue actually has a parent — the fixture has none.
    expect(screen.queryByText("父 issue")).not.toBeInTheDocument();
    // The "+ Add property" affordance is always offered while any
    // optional field is still hidden.
    expect(screen.getByText("添加字段")).toBeInTheDocument();
  });

  it("hides every optional property row when none are set", async () => {
    // Override the default fixture: nothing optional set.
    mockApiObj.getIssue.mockResolvedValue({
      ...mockIssue,
      priority: "none",
      start_date: null,
      due_date: null,
    });

    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByText("属性")).toBeInTheDocument();
    });

    expect(screen.queryByText("优先级")).not.toBeInTheDocument();
    expect(screen.queryByText("截止日期")).not.toBeInTheDocument();
    expect(screen.queryByText("标签")).not.toBeInTheDocument();
    // Project stays as a core row regardless of value.
    expect(screen.getByTestId("project-picker")).toBeInTheDocument();
    // 没有父 issue 时，也不显示独立的父 issue 区域。
    expect(screen.queryByText("父 issue")).not.toBeInTheDocument();
    expect(screen.getByText("添加字段")).toBeInTheDocument();
  });

  it("uses a non-resizable layout with the sidebar sheet closed by default on mobile", async () => {
    mockViewport.isMobile = true;

    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByDisplayValue("Implement authentication")).toBeInTheDocument();
    });

    expect(screen.queryByTestId("panel-group")).not.toBeInTheDocument();
    expect(screen.queryByText("属性")).not.toBeInTheDocument();
  });

  it("does not render raw metadata controls when the bag has keys", async () => {
    // Metadata is agent-facing; raw keys stay out of the human-facing sidebar.
    mockApiObj.getIssue.mockResolvedValue({
      ...mockIssue,
      metadata: {
        pr_url: "https://example.com/pr/1",
        pipeline_status: "running",
      },
    });

    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByText("详情")).toBeInTheDocument();
    });

    expect(screen.queryByRole("button", { name: /^元数据\b/ })).not.toBeInTheDocument();
    expect(screen.queryByText("pr_url")).not.toBeInTheDocument();
    expect(screen.queryByText("pipeline_status")).not.toBeInTheDocument();
  });

  it("renders TAPD source metadata as a lightweight issue reference", async () => {
    const tapdURL = "https://www.tapd.cn/47654106/markdown_wikis/show/#1147654106001004154";
    mockApiObj.getIssue.mockResolvedValue({
      ...mockIssue,
      metadata: {
        source_provider: "tapd",
        source_url: tapdURL,
        tapd_workspace_id: "47654106",
        tapd_resource_type: "markdown_wiki",
        tapd_resource_id: "1147654106001004154",
        source_fetch_status: "fetched",
        source_fetch_title: "用户快捷入口需求",
        source_fetch_summary: "支持用户管理个人快捷入口，并由 SOP 流程推进实现。",
      },
    });

    renderIssueDetail();

    const card = await screen.findByTestId("tapd-source-card");
    const editor = screen.getByDisplayValue("Add JWT auth to the backend");
    expect(within(card).getByText("TAPD 来源")).toBeInTheDocument();
    expect(within(card).getByTestId("tapd-source-badge")).toBeInTheDocument();
    expect(within(card).getByText("TAPD Wiki")).toBeInTheDocument();
    expect(within(card).getByText("ID 1147654106001004154")).toBeInTheDocument();
    expect(within(card).getByText("已抓取")).toBeInTheDocument();
    expect(within(card).getByTestId("tapd-source-title")).toHaveTextContent("用户快捷入口需求");
    expect(within(card).getByText(/支持用户管理个人快捷入口/)).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: /用户快捷入口需求/ })).toHaveAttribute("href", tapdURL);
    expect(editor.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("labels TAPD Story source metadata in the issue reference", async () => {
    const tapdURL = "https://www.tapd.cn/51081496/prong/stories/view/1151081496001028216";
    mockApiObj.getIssue.mockResolvedValue({
      ...mockIssue,
      metadata: {
        source_provider: "tapd",
        source_url: tapdURL,
        tapd_workspace_id: "51081496",
        tapd_resource_type: "story",
        tapd_resource_id: "1151081496001028216",
        source_fetch_status: "fetched",
        source_fetch_title: "【DSM】【系统管理】公告管理",
        source_fetch_summary: "公告列表提供公告管理查询功能。",
      },
    });

    renderIssueDetail();

    const card = await screen.findByTestId("tapd-source-card");
    expect(within(card).getByText("TAPD Story")).toBeInTheDocument();
    expect(within(card).getByText("ID 1151081496001028216")).toBeInTheDocument();
    expect(within(card).getByTestId("tapd-source-title")).toHaveTextContent("【DSM】【系统管理】公告管理");
    expect(within(card).getByRole("link", { name: /公告管理/ })).toHaveAttribute("href", tapdURL);
  });

  it("does not open a metadata JSON dialog from the sidebar", async () => {
    mockApiObj.getIssue.mockResolvedValue({
      ...mockIssue,
      metadata: {
        pr_url: "https://example.com/pr/1",
        pipeline_status: "running",
      },
    });

    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByText("详情")).toBeInTheDocument();
    });

    expect(screen.queryByRole("button", { name: /^元数据\b/ })).not.toBeInTheDocument();
    expect(document.querySelector("pre")).toBeNull();
  });

  it("hides the Metadata button entirely when the bag is empty", async () => {
    // Default fixture already has metadata: {}, asserted explicitly here.
    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByText("详情")).toBeInTheDocument();
    });

    expect(screen.queryByRole("button", { name: /^元数据\b/ })).not.toBeInTheDocument();
  });

  it("renders the run review card without keeping the completed execution log", async () => {
    mockApiObj.listTasksByIssue.mockResolvedValue([
      {
        id: "task-1",
        agent_id: "agent-1",
        runtime_id: "runtime-1",
        issue_id: "issue-1",
        status: "completed",
        priority: 0,
        dispatched_at: "2026-06-08T08:01:00Z",
        started_at: "2026-06-08T08:02:00Z",
        completed_at: "2026-06-08T08:07:00Z",
        result: null,
        error: null,
        created_at: "2026-06-08T08:00:00Z",
        trigger_summary: "从评论启动",
      },
    ]);

    renderIssueDetail();

    expect(await screen.findByTestId("issue-run-review-summary-card")).toBeInTheDocument();
    expect(screen.queryByTestId("issue-execution-log-section")).not.toBeInTheDocument();
  });

  it("renders Details section with Created by and dates", async () => {
    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByText("详情")).toBeInTheDocument();
    });

    expect(screen.queryByText("创建者")).not.toBeInTheDocument();
    fireEvent.click(screen.getByText("详情"));

    expect(screen.getByText("创建者")).toBeInTheDocument();
    expect(screen.getByText("创建时间")).toBeInTheDocument();
    expect(screen.getByText("更新时间")).toBeInTheDocument();
  });

  it("任务不存在时显示未找到信息", async () => {
    mockApiObj.getIssue.mockRejectedValue(new Error("Not found"));

    renderIssueDetail("nonexistent-id");

    await waitFor(() => {
      expect(
        screen.getByText("这个任务不存在或已在该工作区被删除。"),
      ).toBeInTheDocument();
    });
  });

  it("issue 未找到且没有 onDelete prop 时显示返回任务列表按钮", async () => {
    mockApiObj.getIssue.mockRejectedValue(new Error("Not found"));

    renderIssueDetail("nonexistent-id");

    await waitFor(() => {
      expect(screen.getByText("返回任务列表")).toBeInTheDocument();
    });
  });

  it("渲染动态区标题", async () => {
    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getAllByText("动态").length).toBeGreaterThanOrEqual(1);
    });
  });

  it("渲染时间线评论", async () => {
    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByText("Started working on this")).toBeInTheDocument();
    });

    expect(screen.getByText("I can help with this")).toBeInTheDocument();
  });

  it("从智能体失败评论重跑来源 task", async () => {
    mockApiObj.listTimeline.mockResolvedValue([
      ...mockTimeline,
      {
        type: "comment",
        id: "comment-failed-task",
        actor_type: "agent",
        actor_id: "agent-1",
        content: "API Error: 500 Internal server error",
        parent_id: null,
        created_at: "2026-01-18T00:00:00Z",
        updated_at: "2026-01-18T00:00:00Z",
        comment_type: "system",
        source_task_id: "task-failed",
      },
    ]);

    renderIssueDetail();

    await screen.findByText("API Error: 500 Internal server error");
    fireEvent.click(screen.getByRole("button", { name: "重试任务" }));

    await waitFor(() => {
      expect(mockApiObj.rerunIssue).toHaveBeenCalledWith("issue-1", "task-failed");
    });
  });

  it("子 issue 完成的系统评论不显示重试", async () => {
    mockApiObj.listTimeline.mockResolvedValue([
      ...mockTimeline,
      {
        type: "comment",
        id: "comment-child-done",
        actor_type: "system",
        actor_id: "00000000-0000-0000-0000-000000000000",
        content: "子任务 MUL-123 已完成。",
        parent_id: null,
        created_at: "2026-01-18T00:00:00Z",
        updated_at: "2026-01-18T00:00:00Z",
        comment_type: "system",
      },
    ]);

    renderIssueDetail();

    await screen.findByText("子任务 MUL-123 已完成。");
    expect(screen.queryByRole("button", { name: "重试任务" })).not.toBeInTheDocument();
  });

  it("成功的智能体 task 评论不显示重试", async () => {
    mockApiObj.listTimeline.mockResolvedValue([
      ...mockTimeline,
      {
        type: "comment",
        id: "comment-successful-task",
        actor_type: "agent",
        actor_id: "agent-1",
        content: "Finished the requested work.",
        parent_id: null,
        created_at: "2026-01-18T00:00:00Z",
        updated_at: "2026-01-18T00:00:00Z",
        comment_type: "comment",
        source_task_id: "task-success",
      },
    ]);

    renderIssueDetail();

    await screen.findByText("Finished the requested work.");
    expect(screen.queryByRole("button", { name: "重试任务" })).not.toBeInTheDocument();
  });

  it("没有来源 task 的智能体系统评论不显示重试", async () => {
    mockApiObj.listTimeline.mockResolvedValue([
      ...mockTimeline,
      {
        type: "comment",
        id: "comment-agent-system",
        actor_type: "agent",
        actor_id: "agent-1",
        content: "System coordination update.",
        parent_id: null,
        created_at: "2026-01-18T00:00:00Z",
        updated_at: "2026-01-18T00:00:00Z",
        comment_type: "system",
      },
    ]);

    renderIssueDetail();

    await screen.findByText("System coordination update.");
    expect(screen.queryByRole("button", { name: "重试任务" })).not.toBeInTheDocument();
  });

  it("折叠非尾部动态块，并默认展开最后一个动态块", async () => {
    // Timeline shape:
    //   [activities: status_changed, priority_changed] ← block A (older)
    //   [comment-1]
    //   [activities: due_date_changed]                  ← block B (latest)
    // Block A should be collapsed; block B should be expanded.
    mockApiObj.listTimeline.mockResolvedValue([
      {
        type: "activity",
        id: "act-1",
        actor_type: "member",
        actor_id: "user-1",
        action: "status_changed",
        details: { from: "todo", to: "in_progress" },
        created_at: "2026-01-16T00:00:00Z",
      },
      {
        type: "activity",
        id: "act-2",
        actor_type: "member",
        actor_id: "user-1",
        action: "priority_changed",
        details: { from: "low", to: "high" },
        created_at: "2026-01-16T01:00:00Z",
      },
      {
        type: "comment",
        id: "comment-1",
        actor_type: "member",
        actor_id: "user-1",
        content: "Talking it through",
        parent_id: null,
        created_at: "2026-01-17T00:00:00Z",
        updated_at: "2026-01-17T00:00:00Z",
        comment_type: "comment",
      },
      {
        type: "activity",
        id: "act-3",
        actor_type: "member",
        actor_id: "user-1",
        action: "due_date_changed",
        details: { to: "2026-02-01T00:00:00Z" },
        created_at: "2026-01-18T00:00:00Z",
      },
    ] as TimelineEntry[]);

    renderIssueDetail();

    // Latest block (single activity) is expanded — its rendered text is visible.
    await waitFor(() => {
      expect(screen.getByText(/截止日期设为/)).toBeInTheDocument();
    });

    // Older block is collapsed: shows the summary, hides the individual entries.
    expect(screen.getByText("2 条动态")).toBeInTheDocument();
    expect(screen.queryByText(/状态从/)).not.toBeInTheDocument();
    expect(screen.queryByText(/优先级从/)).not.toBeInTheDocument();

    // Clicking the summary expands the older block.
    fireEvent.click(screen.getByText("2 条动态"));
    await waitFor(() => {
      expect(screen.getByText(/状态从/)).toBeInTheDocument();
    });
    expect(screen.getByText(/优先级从/)).toBeInTheDocument();
  });

  it("未知状态值的动态行也能正常渲染", async () => {
    mockApiObj.listTimeline.mockResolvedValue([
      {
        type: "activity",
        id: "act-unknown-status",
        actor_type: "member",
        actor_id: "user-1",
        action: "status_changed",
        details: { from: "todo", to: "mystery_status" },
        created_at: "2026-01-18T00:00:00Z",
      },
    ] as TimelineEntry[]);

    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByText(/状态从 待办 改为 mystery_status/)).toBeInTheDocument();
    });
  });

  it("将尾部动态块截断为最近 8 条，并显示展开更多开关", async () => {
    // 10 activities, all in the trailing block (no comment after them, so it's
    // the trailing block by definition). Alternating action types so the
    // 2-minute coalesce window never merges consecutive entries — we end up
    // with 10 distinct rows.
    const trailingBlock: TimelineEntry[] = [
      { type: "activity", id: "act-1", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "todo", to: "in_progress" }, created_at: "2026-01-18T00:00:00Z" },
      { type: "activity", id: "act-2", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "low", to: "medium" }, created_at: "2026-01-18T00:01:00Z" },
      { type: "activity", id: "act-3", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "in_progress", to: "in_review" }, created_at: "2026-01-18T00:02:00Z" },
      { type: "activity", id: "act-4", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "medium", to: "high" }, created_at: "2026-01-18T00:03:00Z" },
      { type: "activity", id: "act-5", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "in_review", to: "done" }, created_at: "2026-01-18T00:04:00Z" },
      { type: "activity", id: "act-6", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "high", to: "urgent" }, created_at: "2026-01-18T00:05:00Z" },
      { type: "activity", id: "act-7", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "done", to: "blocked" }, created_at: "2026-01-18T00:06:00Z" },
      { type: "activity", id: "act-8", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "urgent", to: "low" }, created_at: "2026-01-18T00:07:00Z" },
      { type: "activity", id: "act-9", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "blocked", to: "todo" }, created_at: "2026-01-18T00:08:00Z" },
      { type: "activity", id: "act-10", actor_type: "member", actor_id: "user-1", action: "due_date_changed", details: { to: "2026-02-01T00:00:00Z" }, created_at: "2026-01-18T00:09:00Z" },
    ] as TimelineEntry[];
    mockApiObj.listTimeline.mockResolvedValue(trailingBlock);

    renderIssueDetail();

    // In the truncated default state the "N activities" collapse header
    // stays hidden — the "Show N more" link is the only control we want
    // to expose for a glance at recent activity.
    await waitFor(() => {
      expect(screen.getByText("展开更早 2 条动态")).toBeInTheDocument();
    });
    expect(screen.queryByText("10 条动态")).not.toBeInTheDocument();

    // Only the 8 most recent entries (act-3..act-10) are rendered by default.
    // act-1 and act-2 are folded behind the show-more line.
    expect(screen.getByText(/状态从 进行中 改为 审核中/)).toBeInTheDocument(); // act-3
    expect(screen.getByText(/截止日期设为/)).toBeInTheDocument(); // act-10
    expect(screen.queryByText(/状态从 待办 改为 进行中/)).not.toBeInTheDocument(); // act-1
    expect(screen.queryByText(/优先级从 低 改为 中/)).not.toBeInTheDocument(); // act-2

    // Clicking the toggle reveals the older entries in place and brings the
    // full "N activities" header back (so the user can fold the block).
    fireEvent.click(screen.getByText("展开更早 2 条动态"));
    await waitFor(() => {
      expect(screen.getByText(/状态从 待办 改为 进行中/)).toBeInTheDocument();
    });
    expect(screen.getByText(/优先级从 低 改为 中/)).toBeInTheDocument();
    expect(screen.getByText(/截止日期设为/)).toBeInTheDocument();
    expect(screen.getByText("10 条动态")).toBeInTheDocument();
    expect(screen.queryByText(/展开更早 \d+ 条动态/)).not.toBeInTheDocument();
  });

  it("尾部动态块不超过 8 条时不显示展开更多开关", async () => {
    const trailingBlock: TimelineEntry[] = [
      { type: "activity", id: "act-1", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "todo", to: "in_progress" }, created_at: "2026-01-18T00:00:00Z" },
      { type: "activity", id: "act-2", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "low", to: "high" }, created_at: "2026-01-18T00:01:00Z" },
      { type: "activity", id: "act-3", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "in_progress", to: "in_review" }, created_at: "2026-01-18T00:02:00Z" },
      { type: "activity", id: "act-4", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "high", to: "urgent" }, created_at: "2026-01-18T00:03:00Z" },
      { type: "activity", id: "act-5", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "in_review", to: "done" }, created_at: "2026-01-18T00:04:00Z" },
      { type: "activity", id: "act-6", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "urgent", to: "low" }, created_at: "2026-01-18T00:05:00Z" },
      { type: "activity", id: "act-7", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "done", to: "blocked" }, created_at: "2026-01-18T00:06:00Z" },
      { type: "activity", id: "act-8", actor_type: "member", actor_id: "user-1", action: "due_date_changed", details: { to: "2026-02-01T00:00:00Z" }, created_at: "2026-01-18T00:07:00Z" },
    ] as TimelineEntry[];
    mockApiObj.listTimeline.mockResolvedValue(trailingBlock);

    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByText("8 条动态")).toBeInTheDocument();
    });
    // Every one of the 8 entries should be visible — the trailing block fits
    // exactly within the limit, so no "Show N more activities" line appears.
    expect(screen.getByText(/状态从 待办 改为 进行中/)).toBeInTheDocument();
    expect(screen.getByText(/优先级从 低 改为 高/)).toBeInTheDocument();
    expect(screen.getByText(/状态从 进行中 改为 审核中/)).toBeInTheDocument();
    expect(screen.getByText(/优先级从 高 改为 紧急/)).toBeInTheDocument();
    expect(screen.getByText(/状态从 审核中 改为 已完成/)).toBeInTheDocument();
    expect(screen.getByText(/优先级从 紧急 改为 低/)).toBeInTheDocument();
    expect(screen.getByText(/状态从 已完成 改为 已阻塞/)).toBeInTheDocument();
    expect(screen.getByText(/截止日期设为/)).toBeInTheDocument();
    expect(screen.queryByText(/展开更早 \d+ 条动态/)).not.toBeInTheDocument();
  });

  it("展开非尾部动态块显示全部条目，只有尾部动态块会截断旧条目", async () => {
    // Non-trailing block (10 activities) + comment + trailing block (1 activity).
    // Manually expanding the older block must reveal all 10 entries — the
    // truncate-to-8 rule applies only to the trailing block.
    const timeline: TimelineEntry[] = [
      { type: "activity", id: "old-1", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "backlog", to: "todo" }, created_at: "2026-01-16T00:00:00Z" },
      { type: "activity", id: "old-2", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "none", to: "low" }, created_at: "2026-01-16T00:01:00Z" },
      { type: "activity", id: "old-3", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "todo", to: "in_progress" }, created_at: "2026-01-16T00:02:00Z" },
      { type: "activity", id: "old-4", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "low", to: "medium" }, created_at: "2026-01-16T00:03:00Z" },
      { type: "activity", id: "old-5", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "in_progress", to: "in_review" }, created_at: "2026-01-16T00:04:00Z" },
      { type: "activity", id: "old-6", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "medium", to: "high" }, created_at: "2026-01-16T00:05:00Z" },
      { type: "activity", id: "old-7", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "in_review", to: "done" }, created_at: "2026-01-16T00:06:00Z" },
      { type: "activity", id: "old-8", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "high", to: "urgent" }, created_at: "2026-01-16T00:07:00Z" },
      { type: "activity", id: "old-9", actor_type: "member", actor_id: "user-1", action: "status_changed", details: { from: "done", to: "blocked" }, created_at: "2026-01-16T00:08:00Z" },
      { type: "activity", id: "old-10", actor_type: "member", actor_id: "user-1", action: "priority_changed", details: { from: "urgent", to: "low" }, created_at: "2026-01-16T00:09:00Z" },
      {
        type: "comment", id: "comment-mid", actor_type: "member", actor_id: "user-1",
        content: "Splitting the blocks", parent_id: null,
        created_at: "2026-01-17T00:00:00Z", updated_at: "2026-01-17T00:00:00Z",
        comment_type: "comment",
      },
      { type: "activity", id: "last-1", actor_type: "member", actor_id: "user-1", action: "due_date_changed", details: { to: "2026-02-01T00:00:00Z" }, created_at: "2026-01-18T00:00:00Z" },
    ] as TimelineEntry[];
    mockApiObj.listTimeline.mockResolvedValue(timeline);

    renderIssueDetail();

    // The older block defaults to collapsed; its summary reports 10.
    await waitFor(() => {
      expect(screen.getByText("10 条动态")).toBeInTheDocument();
    });
    // None of the older entries are rendered before expansion.
    expect(screen.queryByText(/状态从 待规划 改为 待办/)).not.toBeInTheDocument();

    // Expand the older block by clicking its summary line.
    fireEvent.click(screen.getByText("10 条动态"));

    // Every one of the 10 entries should now be visible — even though the
    // block has more than 8 entries, the truncate-to-8 rule does not apply
    // to non-trailing blocks, so no "Show N more activities" line appears.
    await waitFor(() => {
      expect(screen.getByText(/状态从 待规划 改为 待办/)).toBeInTheDocument();
    });
    expect(screen.getByText(/优先级从 无优先级 改为 低/)).toBeInTheDocument();
    expect(screen.getByText(/状态从 待办 改为 进行中/)).toBeInTheDocument();
    expect(screen.getByText(/优先级从 低 改为 中/)).toBeInTheDocument();
    expect(screen.getByText(/状态从 进行中 改为 审核中/)).toBeInTheDocument();
    expect(screen.getByText(/优先级从 中 改为 高/)).toBeInTheDocument();
    expect(screen.getByText(/状态从 审核中 改为 已完成/)).toBeInTheDocument();
    expect(screen.getByText(/优先级从 高 改为 紧急/)).toBeInTheDocument();
    expect(screen.getByText(/状态从 已完成 改为 已阻塞/)).toBeInTheDocument();
    expect(screen.getByText(/优先级从 紧急 改为 低/)).toBeInTheDocument();
    expect(screen.queryByText(/展开更早 \d+ 条动态/)).not.toBeInTheDocument();
  });

  describe("highlightCommentId scroll-to-comment", () => {
    it("issue 和时间线都加载完成后滚动到高亮评论", async () => {
      renderIssueDetailWithHighlight("comment-2");

      // Wait for the comment row to mount. With initialItemCount in
      // production, items[0..targetIdx] are force-mounted on first commit;
      // the mock unconditionally inline-renders every item, so this just
      // waits for the regular render pass.
      await waitFor(() => {
        expect(
          document.getElementById("comment-comment-2"),
        ).not.toBeNull();
      });

      // The deep-link effect lands on AND highlights the target comment: it
      // drives the timeline container's scrollTop directly (jsdom has no
      // layout, so the scroll itself isn't observable here) and applies the
      // brand highlight ring. Assert the user-facing highlight.
      await waitFor(() => {
        expect(
          document.getElementById("comment-comment-2")?.querySelector(".ring-2"),
        ).not.toBeNull();
      });
    });

    it("时间线早于 issue 准备好时仍会滚动", async () => {
      // Reproduces the inbox-click race: timeline data is in the cache
      // before the issue resolves. While loading is true, IssueDetail
      // renders the loading skeleton (the timeline never mounts), so no
      // scroll/highlight can fire. After the issue resolves, the timeline
      // mounts and the deep-link effect lands on + highlights the comment.
      let resolveIssue: (value: Issue) => void = () => {};
      const issuePromise = new Promise<Issue>((resolve) => {
        resolveIssue = resolve;
      });
      mockApiObj.getIssue.mockReturnValue(issuePromise);

      renderIssueDetailWithHighlight("comment-2", "issue-1", { seedTimeline: true });

      expect(
        document.getElementById("comment-comment-2"),
      ).toBeNull();
      // Nothing highlighted while the loading skeleton is up.
      expect(document.querySelector(".ring-2")).toBeNull();

      resolveIssue(mockIssue);

      await waitFor(() => {
        expect(
          document.getElementById("comment-comment-2"),
        ).not.toBeNull();
      });
      await waitFor(() => {
        expect(
          document.getElementById("comment-comment-2")?.querySelector(".ring-2"),
        ).not.toBeNull();
      });
    });

    it("深链目标在已折叠已解决线程的回复中时自动展开", async () => {
      // Seed a timeline where comment-3 is resolved (so it renders as a
      // resolved-bar by default) and has a reply, reply-1, whose id is the
      // deep-link target. The reply is not in the flat items array — only
      // the resolved-bar root is. The effect must detect this, expand the
      // thread, then on re-run scroll to the reply's id="comment-reply-1" node.
      const timelineWithResolvedThread: TimelineEntry[] = [
        ...mockTimeline,
        {
          type: "comment",
          id: "comment-3",
          actor_type: "member",
          actor_id: "user-1",
          content: "Resolved root",
          parent_id: null,
          created_at: "2026-01-18T00:00:00Z",
          updated_at: "2026-01-18T00:00:00Z",
          comment_type: "comment",
          resolved_at: "2026-01-19T00:00:00Z",
        } as TimelineEntry,
        {
          type: "comment",
          id: "reply-1",
          actor_type: "member",
          actor_id: "user-1",
          content: "Reply inside resolved thread",
          parent_id: "comment-3",
          created_at: "2026-01-18T01:00:00Z",
          updated_at: "2026-01-18T01:00:00Z",
          comment_type: "comment",
        } as TimelineEntry,
      ];
      mockApiObj.listTimeline.mockResolvedValue(timelineWithResolvedThread);

      const queryClient = createTestQueryClient();
      render(
        <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
          <QueryClientProvider client={queryClient}>
            <IssueDetail issueId="issue-1" highlightCommentId="reply-1" />
          </QueryClientProvider>
        </I18nProvider>,
      );

      // After expansion, the reply must appear in the DOM (inside the now
      // -unfolded CommentCard) and the deep-link effect must land on + highlight
      // it. The reply highlight renders as a computed bg tint on its row (see
      // CommentCard's reply branch), so assert the row carries the brand tint.
      await waitFor(() => {
        expect(
          document.getElementById("comment-reply-1"),
        ).not.toBeNull();
      });
      await waitFor(() => {
        expect(
          document.getElementById("comment-reply-1")?.className,
        ).toContain("bg-[color-mix(in_srgb,var(--card)_95%,var(--brand)_5%)]");
      });
    });
  });

  it("编辑器清空后发送空描述", async () => {
    renderIssueDetail();

    await waitFor(() => {
      expect(screen.getByDisplayValue("Add JWT auth to the backend")).toBeInTheDocument();
    });

    const editor = screen.getByPlaceholderText("添加描述...");
    fireEvent.change(editor, { target: { value: "" } });

    await waitFor(() => {
      expect(mockApiObj.updateIssue).toHaveBeenCalledWith(
        "issue-1",
        expect.objectContaining({ description: "" }),
      );
    });
  });
});
