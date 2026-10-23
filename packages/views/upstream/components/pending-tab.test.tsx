// 待吸收列表：只读成员的写入口隐藏，创建吸收任务前必须确认。
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";
import type { UpstreamItem, UpstreamSnapshot } from "@multica/core/upstream/queries";
import { PendingTab } from "./pending-tab";

const mockMutate = vi.hoisted(() => vi.fn());
const mockMark = vi.hoisted(() => vi.fn());
const mockPush = vi.hoisted(() => vi.fn());

const ITEM: UpstreamItem = {
  id: "item-1",
  run_id: "run-1",
  cluster_key: "cluster-1",
  title: "支持按目录批量导入",
  summary: "上游新增了一个批量导入入口。",
  rationale: "本地还没有对应实现。",
  verdict: "absorb",
  confidence: "high",
  presence: "absent",
  paths: [],
  commit_count: 2,
  commit_shas: [],
  first_commit_at: null,
  last_commit_at: null,
  status: "open",
  adopted_at: null,
  created_at: "2026-10-20T00:00:00Z",
  absorption_issue_id: "",
  absorption_status: "none",
  has_new_commits: false,
};

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-test" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/issues/${id}` }),
}));
vi.mock("@multica/core/upstream/queries", () => ({
  upstreamItemsOptions: () => ({
    queryKey: ["upstream-items"],
    initialPageParam: 0,
    queryFn: async () => ({ items: [ITEM], total: 1 }),
    getNextPageParam: () => undefined,
  }),
}));
vi.mock("@multica/core/upstream/mutations", () => ({
  useMarkUpstreamItems: () => ({ mutate: mockMark, isPending: false, isError: false, error: null }),
  useCreateUpstreamAbsorptionTask: () => ({ mutate: mockMutate, isPending: false, isError: false, error: null }),
}));
vi.mock("../../navigation/context", () => ({
  useNavigation: () => ({ push: mockPush, prefetch: vi.fn(), replace: vi.fn() }),
}));

function snapshot(canWrite: boolean): UpstreamSnapshot {
  return {
    config: {
      upstream_url: "https://github.com/multica-ai/multica",
      upstream_branch: "main",
      local_ref: "origin/main",
      runtime_id: "rt-1",
      agent_id: "agent-1",
      autopilot_id: "pilot-1",
      batch_size: 200,
      schedule_hour: 9,
      sync_state: "incremental",
      bootstrap_base_sha: "",
      cursor_sha: "",
      head_sha: "",
      last_run_at: "2026-10-22T12:00:00Z",
      error: "",
      model: "",
      thinking_level: "",
      instructions: "",
    },
    pending: 1,
    adopted: 0,
    ignored: 0,
    active_run_id: "",
    next_run_at: null,
    runtime_online: true,
    executor_state: "bound",
    can_write: canWrite,
    last_run: null,
  };
}

function render(canWrite: boolean) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <PendingTab wsId="ws-test" snapshot={snapshot(canWrite)} onOpenSettings={() => {}} />
    </QueryClientProvider>,
    { locale: "zh-Hans" },
  );
}

beforeEach(() => {
  mockMutate.mockReset();
  mockMark.mockReset();
  mockPush.mockReset();
});
afterEach(() => {
  vi.restoreAllMocks();
});

describe("待吸收列表的写入口", () => {
  it("只读成员看不到勾选框和创建任务入口", async () => {
    render(false);
    expect(await screen.findByText("支持按目录批量导入")).toBeInTheDocument();

    // 勾选框还在行里但不可用，批量条与创建入口整片不渲染。
    expect(screen.getByLabelText("支持按目录批量导入")).toHaveAttribute("aria-disabled", "true");
    expect(screen.queryByRole("button", { name: "创建吸收任务" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "标记已采纳" })).not.toBeInTheDocument();
  });

  it("创建吸收任务先要确认，取消时不留任务", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const user = userEvent.setup();
    render(true);

    await user.click(await screen.findByRole("button", { name: "创建吸收任务" }));

    expect(confirm).toHaveBeenCalledTimes(1);
    expect(mockMutate).not.toHaveBeenCalled();
  });

  it("确认后创建任务并打开它", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    mockMutate.mockImplementation((_itemId: string, options?: { onSuccess?: (result: { issue_id: string }) => void }) => {
      options?.onSuccess?.({ issue_id: "issue-9" });
    });
    const user = userEvent.setup();
    render(true);

    await user.click(await screen.findByRole("button", { name: "创建吸收任务" }));

    await waitFor(() => expect(mockMutate).toHaveBeenCalledWith("item-1", expect.anything()));
    expect(mockPush).toHaveBeenCalledWith("/issues/issue-9");
  });
});
