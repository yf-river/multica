import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Issue } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enIssues from "../../locales/zh-Hans/issues.json";

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, issues: enIssues } };
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

// Mock @multica/core/paths — after the URL-driven workspace refactor,
// useCurrentWorkspace derives from the workspace slug in URL Context. Tests
// don't mount a real route, so we short-circuit to a fixed fixture.
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

// Mock @multica/views/navigation (AppLink + useNavigation)
vi.mock("../../navigation", () => ({
  AppLink: ({ children, href, ...props }: any) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
  useNavigation: () => ({ push: vi.fn(), pathname: "/issues" }),
  NavigationProvider: ({ children }: { children: React.ReactNode }) => children,
}));

// Mock workspace avatar
vi.mock("../../workspace/workspace-avatar", () => ({
  WorkspaceAvatar: ({ name }: { name: string }) => <span data-testid="workspace-avatar">{name.charAt(0)}</span>,
}));

// Mock api (queries use api internally)
const mockListIssues = vi.hoisted(() => vi.fn().mockResolvedValue({ issues: [], total: 0 }));
const mockListIssueBuckets = vi.hoisted(() => vi.fn().mockResolvedValue({ by_status: {} }));
const mockListGroupedIssues = vi.hoisted(() => vi.fn().mockResolvedValue({ groups: [] }));
const mockListMembers = vi.hoisted(() =>
  vi.fn().mockResolvedValue([
    {
      id: "member-1",
      workspace_id: "ws-1",
      user_id: "user-1",
      role: "member",
      created_at: "2026-01-01T00:00:00Z",
      name: "Test User",
      account: "test",
      avatar_url: null,
    },
  ]),
);
const mockListAgents = vi.hoisted(() =>
  vi.fn().mockResolvedValue([
    {
      id: "agent-1",
      workspace_id: "ws-1",
      name: "Agent One",
      description: "",
      instructions: "",
      status: "idle",
      runtime_id: null,
      owner_id: "user-1",
      avatar_url: null,
      visibility: "workspace",
      archived_at: null,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
  ]),
);
const mockListSquads = vi.hoisted(() =>
  vi.fn().mockResolvedValue([
    {
      id: "squad-1",
      workspace_id: "ws-1",
      name: "Squad One",
      description: "",
      instructions: "",
      avatar_url: null,
      leader_id: "agent-1",
      creator_id: "user-1",
      archived_at: null,
      archived_by: null,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
  ]),
);
const mockGetAssigneeFrequency = vi.hoisted(() => vi.fn().mockResolvedValue([]));
const mockGetChildIssueProgress = vi.hoisted(() => vi.fn().mockResolvedValue({ progress: [] }));
const mockGetAgentTaskSnapshot = vi.hoisted(() => vi.fn().mockResolvedValue([]));
const mockListProjects = vi.hoisted(() => vi.fn().mockResolvedValue({ projects: [], total: 0 }));
const mockListLabels = vi.hoisted(() => vi.fn().mockResolvedValue({ labels: [] }));
vi.mock("@multica/core/api", () => ({
  api: {
    getBaseUrl: () => "http://127.0.0.1:8080",
    listIssues: (...args: any[]) => mockListIssues(...args),
    listIssueBuckets: (...args: any[]) => mockListIssueBuckets(...args),
    listGroupedIssues: (...args: any[]) => mockListGroupedIssues(...args),
    updateIssue: vi.fn(),
    listMembers: (...args: any[]) => mockListMembers(...args),
    listAgents: (...args: any[]) => mockListAgents(...args),
    listSquads: (...args: any[]) => mockListSquads(...args),
    getAssigneeFrequency: (...args: any[]) => mockGetAssigneeFrequency(...args),
    getChildIssueProgress: (...args: any[]) => mockGetChildIssueProgress(...args),
    getAgentTaskSnapshot: (...args: any[]) => mockGetAgentTaskSnapshot(...args),
    listProjects: (...args: any[]) => mockListProjects(...args),
    listLabels: (...args: any[]) => mockListLabels(...args),
  },
  getApi: () => ({
    listIssues: (...args: any[]) => mockListIssues(...args),
    listIssueBuckets: (...args: any[]) => mockListIssueBuckets(...args),
    listGroupedIssues: (...args: any[]) => mockListGroupedIssues(...args),
    updateIssue: vi.fn(),
    listMembers: (...args: any[]) => mockListMembers(...args),
    listAgents: (...args: any[]) => mockListAgents(...args),
    listSquads: (...args: any[]) => mockListSquads(...args),
    getAssigneeFrequency: (...args: any[]) => mockGetAssigneeFrequency(...args),
    getChildIssueProgress: (...args: any[]) => mockGetChildIssueProgress(...args),
    getAgentTaskSnapshot: (...args: any[]) => mockGetAgentTaskSnapshot(...args),
    listProjects: (...args: any[]) => mockListProjects(...args),
    listLabels: (...args: any[]) => mockListLabels(...args),
  }),
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
    urgent: { label: "Urgent", bars: 4, color: "text-destructive" },
    high: { label: "High", bars: 3, color: "text-warning" },
    medium: { label: "Medium", bars: 2, color: "text-warning" },
    low: { label: "Low", bars: 1, color: "text-info" },
    none: { label: "No priority", bars: 0, color: "text-muted-foreground" },
  },
}));

// Mock view store
const mockViewState = {
  viewMode: "board" as "board" | "list",
  grouping: "status" as "status" | "assignee",
  statusFilters: [] as string[],
  priorityFilters: [] as string[],
  assigneeFilters: [] as { type: string; id: string }[],
  includeNoAssignee: false,
  creatorFilters: [] as { type: string; id: string }[],
  projectFilters: [] as string[],
  includeNoProject: false,
  labelFilters: [] as string[],
  sortBy: "position" as const,
  sortDirection: "asc" as const,
  cardProperties: { priority: true, description: true, assignee: true, dueDate: true, project: true, childProgress: true, labels: true },
  listCollapsedStatuses: [] as string[],
  setViewMode: vi.fn(),
  setGrouping: vi.fn(),
  toggleStatusFilter: vi.fn(),
  togglePriorityFilter: vi.fn(),
  toggleAssigneeFilter: vi.fn(),
  toggleNoAssignee: vi.fn(),
  toggleCreatorFilter: vi.fn(),
  toggleProjectFilter: vi.fn(),
  toggleNoProject: vi.fn(),
  toggleLabelFilter: vi.fn(),
  hideStatus: vi.fn(),
  showStatus: vi.fn(),
  clearFilters: vi.fn(),
  setSortBy: vi.fn(),
  setSortDirection: vi.fn(),
  toggleCardProperty: vi.fn(),
  toggleListCollapsed: vi.fn(),
};

vi.mock("@multica/core/issues/stores/view-store", () => ({
  useClearFiltersOnWorkspaceChange: () => {},
  viewStorePersistOptions: () => ({ name: "test", storage: undefined, partialize: (s: any) => s }),
  mergeViewStatePersisted: (_p: unknown, c: any) => c,
  viewStoreSlice: vi.fn(),
  useIssueViewStore: Object.assign(
    (selector?: any) => (selector ? selector(mockViewState) : mockViewState),
    { getState: () => mockViewState, setState: vi.fn() },
  ),
  createIssueViewStore: () => ({
    getState: () => mockViewState,
    setState: vi.fn(),
    subscribe: vi.fn(),
  }),
  SORT_OPTIONS: [
    { value: "position", label: "Manual" },
    { value: "priority", label: "优先级" },
    { value: "due_date", label: "截止日期" },
    { value: "created_at", label: "Created date" },
    { value: "title", label: "Title" },
  ],
  GROUPING_OPTIONS: [
    { value: "status", label: "状态" },
    { value: "assignee", label: "负责人" },
  ],
  CARD_PROPERTY_OPTIONS: [
    { key: "priority", label: "优先级" },
    { key: "description", label: "Description" },
    { key: "assignee", label: "负责人" },
    { key: "dueDate", label: "截止日期" },
    { key: "project", label: "Project" },
    { key: "labels", label: "标签" },
    { key: "childProgress", label: "Sub-issue progress" },
  ],
}));

vi.mock("@multica/core/issues/stores/view-store-context", () => ({
  ViewStoreProvider: ({ children }: { children: React.ReactNode }) => children,
  useViewStore: (selector?: any) => (selector ? selector(mockViewState) : mockViewState),
  useViewStoreApi: () => ({ getState: () => mockViewState, setState: vi.fn(), subscribe: vi.fn() }),
}));

let mockScope = "all";

vi.mock("@multica/core/issues/stores/issues-scope-store", () => ({
  useIssuesScopeStore: Object.assign(
    (selector?: any) => {
      const state = { scope: mockScope, setScope: vi.fn() };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ scope: mockScope, setScope: vi.fn() }) },
  ),
}));

vi.mock("@multica/core/issues/stores/selection-store", () => ({
  useIssueSelectionStore: Object.assign(
    (selector?: any) => {
      const state = { selectedIds: new Set(), toggle: vi.fn(), clear: vi.fn(), setAll: vi.fn() };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ selectedIds: new Set(), toggle: vi.fn(), clear: vi.fn(), setAll: vi.fn() }) },
  ),
}));

vi.mock("@multica/core/issues/stores/recent-issues-store", () => ({
  useRecentIssuesStore: Object.assign(
    (selector?: any) => {
      const state = { byWorkspace: {}, recordVisit: vi.fn(), pruneWorkspaces: vi.fn() };
      return selector ? selector(state) : state;
    },
    {
      getState: () => ({
        byWorkspace: {},
        recordVisit: vi.fn(),
        pruneWorkspaces: vi.fn(),
      }),
    },
  ),
  selectRecentIssues: () => () => [],
}));

vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(
    () => ({ open: vi.fn() }),
    { getState: () => ({ open: vi.fn() }) },
  ),
}));

// Mock sonner toast
vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

// Mock dnd-kit
vi.mock("@dnd-kit/core", () => ({
  DndContext: ({ children }: any) => children,
  DragOverlay: () => null,
  PointerSensor: class {},
  useSensor: () => ({}),
  useSensors: () => [],
  useDroppable: () => ({ setNodeRef: vi.fn(), isOver: false }),
  pointerWithin: vi.fn(),
  closestCenter: vi.fn(),
}));

vi.mock("@dnd-kit/sortable", () => ({
  SortableContext: ({ children }: any) => children,
  verticalListSortingStrategy: {},
  arrayMove: vi.fn(),
  useSortable: () => ({
    attributes: {},
    listeners: {},
    setNodeRef: vi.fn(),
    transform: null,
    transition: null,
    isDragging: false,
  }),
}));

vi.mock("@dnd-kit/utilities", () => ({
  CSS: { Transform: { toString: () => undefined } },
}));

// Mock @base-ui/react/accordion (used by ListView)
vi.mock("@base-ui/react/accordion", () => ({
  Accordion: Object.assign(
    ({ children }: any) => <div>{children}</div>,
    {
      Root: ({ children }: any) => <div>{children}</div>,
      Item: ({ children }: any) => <div>{children}</div>,
      Header: ({ children }: any) => <div>{children}</div>,
      Trigger: ({ children }: any) => <button>{children}</button>,
      Panel: ({ children }: any) => <div>{children}</div>,
    },
  ),
}));

// ---------------------------------------------------------------------------
// Test data
// ---------------------------------------------------------------------------

const issueDefaults = {
  parent_issue_id: null,
  project_id: null,
  position: 0,
  metadata: {},
};

const mockIssues: Issue[] = [
  {
    ...issueDefaults,
    id: "issue-1",
    workspace_id: "ws-1",
    number: 1,
    identifier: "TES-1",
    title: "Implement auth",
    description: "Add JWT authentication",
    status: "todo",
    priority: "high",
    assignee_type: "member",
    assignee_id: "user-1",
    creator_type: "member",
    creator_id: "user-1",
    start_date: null,
    due_date: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    ...issueDefaults,
    id: "issue-2",
    workspace_id: "ws-1",
    number: 2,
    identifier: "TES-2",
    title: "Design landing page",
    description: null,
    status: "in_progress",
    priority: "medium",
    assignee_type: "agent",
    assignee_id: "agent-1",
    creator_type: "member",
    creator_id: "user-1",
    start_date: null,
    due_date: "2026-02-01T00:00:00Z",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    ...issueDefaults,
    id: "issue-3",
    workspace_id: "ws-1",
    number: 3,
    identifier: "TES-3",
    title: "Write tests",
    description: null,
    status: "backlog",
    priority: "low",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    start_date: null,
    due_date: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    ...issueDefaults,
    id: "issue-4",
    workspace_id: "ws-1",
    number: 4,
    identifier: "TES-4",
    title: "Squad task",
    description: null,
    status: "todo",
    priority: "medium",
    assignee_type: "squad",
    assignee_id: "squad-1",
    creator_type: "member",
    creator_id: "user-1",
    start_date: null,
    due_date: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
];

function mockIssueBuckets(issues: Issue[]) {
  const by_status: Record<string, { issues: Issue[]; total: number }> = {};
  for (const issue of issues) {
    const bucket = by_status[issue.status] ?? { issues: [], total: 0 };
    bucket.issues.push(issue);
    bucket.total = bucket.issues.length;
    by_status[issue.status] = bucket;
  }
  return { by_status };
}

function mockAssigneeGroups(issues: Issue[]) {
  const groups = new Map<string, { assignee_type: Issue["assignee_type"]; assignee_id: string | null; issues: Issue[] }>();
  for (const issue of issues) {
    const id =
      issue.assignee_type && issue.assignee_id
        ? `assignee:${issue.assignee_type}:${issue.assignee_id}`
        : "assignee:unassigned";
    if (!groups.has(id)) {
      groups.set(id, {
        assignee_type: issue.assignee_type,
        assignee_id: issue.assignee_id,
        issues: [],
      });
    }
    groups.get(id)!.issues.push(issue);
  }
  return {
    groups: [...groups.entries()].map(([id, group]) => ({
      id,
      assignee_type: group.assignee_type,
      assignee_id: group.assignee_id,
      issues: group.issues,
      total: group.issues.length,
    })),
  };
}

// ---------------------------------------------------------------------------
// Import component under test (after mocks)
// ---------------------------------------------------------------------------

import { IssuesPage } from "./issues-page";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function renderWithQuery(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  });
  return render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <QueryClientProvider client={qc}>
        {ui}
      </QueryClientProvider>
    </I18nProvider>,
  );
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("IssuesPage (shared)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockListIssues.mockResolvedValue({ issues: [], total: 0 });
    mockListIssueBuckets.mockResolvedValue({ by_status: {} });
    mockListGroupedIssues.mockResolvedValue({ groups: [] });
    mockGetAssigneeFrequency.mockResolvedValue([]);
    mockGetChildIssueProgress.mockResolvedValue({ progress: [] });
    mockGetAgentTaskSnapshot.mockResolvedValue([]);
    mockListProjects.mockResolvedValue({ projects: [], total: 0 });
    mockListLabels.mockResolvedValue({ labels: [] });
    mockViewState.viewMode = "board";
    mockViewState.grouping = "status";
    mockViewState.statusFilters = [];
    mockViewState.priorityFilters = [];
    mockScope = "all";
  });

  it("shows loading skeletons initially", () => {
    renderWithQuery(<IssuesPage />);
    expect(
      screen.getAllByRole("generic").some((el) => el.getAttribute("data-slot") === "skeleton"),
    ).toBe(true);
  });

  it("renders issue titles after data loads", async () => {
    mockListIssueBuckets.mockResolvedValue(mockIssueBuckets(mockIssues));

    renderWithQuery(<IssuesPage />);

    await screen.findByText("Implement auth");
    expect(screen.getByText("Design landing page")).toBeInTheDocument();
    expect(screen.getByText("Write tests")).toBeInTheDocument();
  });

  it("does not load assignee frequency before an assignee picker opens", async () => {
    mockListIssueBuckets.mockResolvedValue(mockIssueBuckets(mockIssues));

    renderWithQuery(<IssuesPage />);

    await screen.findByText("Implement auth");
    expect(mockGetAssigneeFrequency).not.toHaveBeenCalled();
  });

  it("does not load filter directory data before the filter menu opens", async () => {
    mockListIssueBuckets.mockResolvedValue({ by_status: {} });

    renderWithQuery(<IssuesPage />);

    await screen.findByText("还没有任务");
    expect(mockListMembers).not.toHaveBeenCalled();
    expect(mockListAgents).not.toHaveBeenCalled();
    expect(mockListSquads).not.toHaveBeenCalled();
    expect(mockListProjects).not.toHaveBeenCalled();
    expect(mockListLabels).not.toHaveBeenCalled();
  });

  it("renders board column headers", async () => {
    mockListIssueBuckets.mockResolvedValue(mockIssueBuckets(mockIssues));

    renderWithQuery(<IssuesPage />);

    await screen.findByText("待规划");
    expect(screen.getAllByText("待办").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("进行中").length).toBeGreaterThanOrEqual(1);
  });

  it("groups board columns by assignee", async () => {
    mockViewState.grouping = "assignee";
    mockListGroupedIssues.mockResolvedValue(mockAssigneeGroups(mockIssues));

    renderWithQuery(<IssuesPage />);

    // "Test User" renders both as the assignee group header and on the
    // assignee chip of each card grouped under that header, so a unique
    // match is not guaranteed.
    await screen.findAllByText("Test User");
    expect(screen.getAllByText("Agent One").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("Squad One").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("未分配")).toBeInTheDocument();
  });

  it("uses grouped assignee endpoint instead of status page sweep", async () => {
    mockViewState.grouping = "assignee";
    mockListGroupedIssues.mockResolvedValue(mockAssigneeGroups(mockIssues));

    renderWithQuery(<IssuesPage />);

    await screen.findByText("Implement auth");
    expect(mockListGroupedIssues).toHaveBeenCalledWith(
      expect.objectContaining({
        group_by: "assignee",
        limit: 50,
        offset: 0,
        statuses: ["backlog", "todo", "in_progress", "in_review", "done", "blocked"],
      }),
    );
    expect(mockListIssueBuckets).not.toHaveBeenCalled();
  });

  it("shows the issue section header without a workspace prefix", async () => {
    mockListIssueBuckets.mockResolvedValue(mockIssueBuckets(mockIssues));

    renderWithQuery(<IssuesPage />);

    await screen.findByText("任务");
    // The list header is now `icon + title`, matching the other list pages.
    // The workspace/org name is no longer rendered as a breadcrumb prefix.
    expect(screen.queryByText("Test WS")).not.toBeInTheDocument();
  });

  it("shows empty state when there are no issues", async () => {
    mockListIssueBuckets.mockResolvedValue({ by_status: {} });

    renderWithQuery(<IssuesPage />);

    await screen.findByText("还没有任务");
    expect(screen.getByText("创建一个任务开始使用。")).toBeInTheDocument();
  });

  it("shows scope tab buttons", async () => {
    renderWithQuery(<IssuesPage />);

    expect(await screen.findAllByText("全部")).not.toHaveLength(0);
    expect(screen.getByText("成员")).toBeInTheDocument();
    expect(screen.getByText("智能体")).toBeInTheDocument();
  });

  it("agents scope includes squad-assigned issues", async () => {
    mockScope = "agents";
    mockViewState.viewMode = "list";
    mockListIssueBuckets.mockResolvedValue(mockIssueBuckets(mockIssues));
    renderWithQuery(<IssuesPage />);

    // Squad task and agent task should be visible
    await screen.findByText("Design landing page");
    expect(screen.getByText("Squad task")).toBeInTheDocument();
    // Member task should NOT be visible
    expect(screen.queryByText("Implement auth")).not.toBeInTheDocument();
  });

  it("members scope excludes squad-assigned issues", async () => {
    mockScope = "members";
    mockViewState.viewMode = "list";
    mockListIssueBuckets.mockResolvedValue(mockIssueBuckets(mockIssues));
    renderWithQuery(<IssuesPage />);

    await screen.findByText("Implement auth");
    expect(screen.queryByText("Squad task")).not.toBeInTheDocument();
    expect(screen.queryByText("Design landing page")).not.toBeInTheDocument();
  });
});
