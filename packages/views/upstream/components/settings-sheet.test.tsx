// 上游同步设置面板：提示词编辑、只读成员、运行时离线时的模型只读。
import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";
import type { UpstreamSnapshot } from "@multica/core/upstream/queries";
import { UpstreamSettingsSheet } from "./settings-sheet";

const DEFAULT_TEXT = "内置默认提示词：扫描上游提交，产出待吸收建议。";

const mockSave = vi.hoisted(() => vi.fn());
const runtimes = vi.hoisted(() => [
  { id: "rt-online", name: "mac-online", provider: "codex", status: "online", owner_id: "u1" },
  { id: "rt-offline", name: "mac-offline", provider: "codex", status: "offline", owner_id: "u1" },
]);

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-test" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) => selector({ user: { id: "u1" } }),
}));
vi.mock("@multica/core/runtimes", () => ({
  isRuntimeUsableForUser: () => true,
  runtimeModelsOptions: (runtimeId: string | null) => ({
    queryKey: ["runtime-models", runtimeId],
    queryFn: async () => ({ supported: true, models: [{ id: "gpt-5", label: "gpt-5", provider: "codex" }] }),
    enabled: Boolean(runtimeId),
  }),
}));
vi.mock("@multica/core/runtimes/queries", () => ({
  runtimeListOptions: () => ({ queryKey: ["runtimes"], queryFn: async () => runtimes }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: async () => [] }),
}));
vi.mock("@multica/core/upstream/queries", () => ({
  upstreamInstructionsOptions: () => ({
    queryKey: ["upstream-instructions"],
    queryFn: async () => ({ instructions: DEFAULT_TEXT }),
  }),
}));
vi.mock("@multica/core/upstream/mutations", () => ({
  useUpdateUpstreamConfig: () => ({ mutate: mockSave, isPending: false, isError: false, error: null }),
}));
// 运行时选择器不是这里要验的对象，用原生 select 代替，避免拖进整套弹出层。
vi.mock("../../agents/components/inspector/runtime-picker", () => ({
  RuntimePicker: ({ value, canEdit, onChange }: { value: string; canEdit: boolean; onChange: (id: string) => void }) => (
    <select
      aria-label="运行时"
      value={value}
      disabled={!canEdit}
      onChange={(event) => onChange(event.target.value)}
    >
      <option value="">未选择</option>
      {runtimes.map((runtime) => (
        <option key={runtime.id} value={runtime.id}>
          {runtime.name}
        </option>
      ))}
    </select>
  ),
}));

function snapshot(overrides: { runtimeId?: string; instructions?: string; canWrite?: boolean } = {}): UpstreamSnapshot {
  return {
    config: {
      upstream_url: "https://github.com/multica-ai/multica",
      upstream_branch: "main",
      local_ref: "origin/main",
      runtime_id: overrides.runtimeId ?? "rt-online",
      agent_id: "agent-1",
      autopilot_id: "pilot-1",
      batch_size: 200,
      schedule_hour: 9,
      sync_state: "incremental",
      bootstrap_base_sha: "",
      cursor_sha: "",
      head_sha: "",
      last_run_at: null,
      error: "",
      model: "",
      thinking_level: "",
      instructions: overrides.instructions ?? "",
    },
    pending: 0,
    adopted: 0,
    ignored: 0,
    active_run_id: "",
    next_run_at: null,
    runtime_online: true,
    executor_state: "bound",
    can_write: overrides.canWrite ?? true,
    last_run: null,
  };
}

function render(value: UpstreamSnapshot) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <UpstreamSettingsSheet open onClose={() => {}} snapshot={value} />
    </QueryClientProvider>,
    { locale: "zh-Hans" },
  );
}

beforeEach(() => {
  mockSave.mockReset();
});

describe("上游同步设置的提示词编辑", () => {
  it("清空后不会回填默认提示词，保存按空值提交", async () => {
    const user = userEvent.setup();
    render(snapshot());
    const textarea = await screen.findByLabelText("提示词");
    await waitFor(() => expect(textarea).toHaveValue(DEFAULT_TEXT));

    await user.clear(textarea);

    // 关键回归：清空后仍是空的，不会立刻变回整段内置提示词。
    expect(textarea).toHaveValue("");
    expect(screen.getByText(/已清空，保存后跟随内置默认提示词/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(mockSave).toHaveBeenCalledTimes(1);
    expect(mockSave.mock.calls[0]?.[0]).toMatchObject({ instructions: "" });
  });

  it("恢复默认把输入框还原成内置提示词，并回到未自定义状态", async () => {
    const user = userEvent.setup();
    render(snapshot({ instructions: "我的准则：只关注 packages/views。" }));
    const textarea = await screen.findByLabelText("提示词");
    await waitFor(() => expect(textarea).toHaveValue("我的准则：只关注 packages/views。"));
    expect(screen.getByText(/已自定义/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "恢复默认" }));

    expect(textarea).toHaveValue(DEFAULT_TEXT);
    expect(screen.getByText(/当前跟随内置默认提示词/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(mockSave.mock.calls[0]?.[0]).toMatchObject({ instructions: "" });
  });
});

describe("上游同步设置的边界状态", () => {
  it("运行时离线时模型选择只读并给出提示", async () => {
    render(snapshot({ runtimeId: "rt-offline" }));
    const textarea = await screen.findByLabelText("提示词");
    await waitFor(() => expect(textarea).toHaveValue(DEFAULT_TEXT));

    // 可编辑时模型是一个带「模型 · 默认」标题的按钮；离线时退化成静态文字。
    expect(screen.queryByRole("button", { name: /^模型 · / })).not.toBeInTheDocument();
    expect(screen.getByText("运行时离线时沿用已保存的模型，上线后可重新发现本机目录。")).toBeInTheDocument();
  });

  it("只读成员看到的所有输入都不可编辑", async () => {
    render(snapshot({ canWrite: false }));
    const textarea = await screen.findByLabelText("提示词");
    await waitFor(() => expect(textarea).toHaveValue(DEFAULT_TEXT));

    expect(textarea).toBeDisabled();
    expect(screen.getByLabelText("上游仓库")).toBeDisabled();
    expect(screen.getByLabelText("每批提交数")).toBeDisabled();
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    expect(screen.getByText("只有工作区管理员可以修改这些设置，当前为只读。")).toBeInTheDocument();
  });
});
