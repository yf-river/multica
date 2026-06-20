import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AgentRuntime } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enOnboarding from "../../locales/zh-Hans/onboarding.json";

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, onboarding: enOnboarding } };

const mocks = vi.hoisted(() => ({
  pickerState: {
    runtimes: [] as AgentRuntime[],
    selected: null as AgentRuntime | null,
    selectedId: null as string | null,
    setSelectedId: vi.fn<(id: string) => void>(),
    hasRuntimes: false,
  },
}));

// Swap out the runtime picker so tests can drive runtimes / selection
// without a real TanStack Query + WS stack.
vi.mock("../components/use-runtime-picker", () => ({
  useRuntimePicker: () => mocks.pickerState,
}));

import { StepPlatformFork } from "./step-platform-fork";

function makeRuntime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "rt_test",
    workspace_id: "ws_test",
    name: "Claude Code",
    provider: "claude",
    status: "online",
    runtime_mode: "local",
    runtime_config: {},
    device_info: "",
    metadata: {},
    daemon_id: null,
    last_seen_at: new Date().toISOString(),
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    ...overrides,
  } as unknown as AgentRuntime;
}

function renderFork(
  overrides: Partial<React.ComponentProps<typeof StepPlatformFork>> = {},
) {
  const onNext = vi.fn();
  render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <StepPlatformFork
        wsId="ws_test"
        onNext={onNext}
        cliInstructions={<div data-testid="cli-instructions">install me</div>}
        {...overrides}
      />
    </I18nProvider>,
  );
  return { onNext };
}

function resetPicker(patch: Partial<typeof mocks.pickerState> = {}) {
  mocks.pickerState.runtimes = patch.runtimes ?? [];
  mocks.pickerState.selected = patch.selected ?? null;
  mocks.pickerState.selectedId = patch.selectedId ?? null;
  mocks.pickerState.hasRuntimes = patch.hasRuntimes ?? false;
  mocks.pickerState.setSelectedId = vi.fn();
}

describe("StepPlatformFork", () => {
  beforeEach(() => {
    resetPicker();
    vi.restoreAllMocks();
  });

  it("静止态渲染三种连接方式", () => {
    renderFork();
    expect(screen.getByText(/^使用这台电脑$/)).toBeInTheDocument();
    expect(screen.getByText(/^通过终端连接$/)).toBeInTheDocument();
    expect(screen.getByText(/^使用云电脑$/)).toBeInTheDocument();
    // Cloud option is a "Coming soon" preview — not yet wired up.
    expect(screen.getByText(/^即将推出$/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^即将推出$/ }),
    ).not.toBeInTheDocument();
    // CLI dialog closed at rest → no CLI instructions.
    expect(screen.queryByTestId("cli-instructions")).not.toBeInTheDocument();
  });

  it("页脚只有暂时跳过和说明提示，没有继续按钮", () => {
    renderFork();
    expect(
      screen.getByRole("button", { name: /暂时跳过/ }),
    ).toBeEnabled();
    // Continue is gone — it lived in the footer before; now advancement
    // for the CLI path is owned by the CLI dialog's own button.
    expect(
      screen.queryByRole("button", { name: /^继续$/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText(/在上方选一种方式/),
    ).toBeInTheDocument();
  });

  it("暂时跳过始终启用并调用 onNext(null)", async () => {
    const user = userEvent.setup();
    const { onNext } = renderFork();
    await user.click(screen.getByRole("button", { name: /暂时跳过/ }));
    expect(onNext).toHaveBeenCalledTimes(1);
    expect(onNext).toHaveBeenCalledWith(null);
  });

  it("打开下载页并把卡片切到点击后状态", async () => {
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    const user = userEvent.setup();
    renderFork();

    await user.click(screen.getByText(/^使用这台电脑$/));

    // Routes to the new /download page (not GitHub releases) so the
    // user lands on the OS auto-detect surface.
    expect(openSpy).toHaveBeenCalledWith(
      "/download",
      "_blank",
      "noopener,noreferrer",
    );
    expect(
      screen.getByText(/正在打开下载页/),
    ).toBeInTheDocument();
  });

  it("CLI 对话框打开后显示说明、等待状态和禁用的连接按钮", async () => {
    const user = userEvent.setup();
    renderFork();

    await user.click(screen.getByRole("button", { name: /查看步骤/ }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByTestId("cli-instructions")).toBeInTheDocument();
    expect(
      within(dialog).getByText(/正在等待你的电脑/),
    ).toBeInTheDocument();
    // 未选择运行时时，开始探索保持禁用。
    expect(
      within(dialog).getByRole("button", { name: /开始探索/ }),
    ).toBeDisabled();
  });

  it("CLI 对话框已选择运行时时启用连接并触发 onNext(runtime)", async () => {
    const rt = makeRuntime({ id: "rt_claude", name: "Claude Code" });
    resetPicker({
      runtimes: [rt],
      selected: rt,
      selectedId: rt.id,
      hasRuntimes: true,
    });
    const user = userEvent.setup();
    const { onNext } = renderFork();

    await user.click(screen.getByRole("button", { name: /查看步骤/ }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/已连接 1 台电脑/)).toBeInTheDocument();
    expect(
      within(dialog).getByText(/已选择：Claude Code/),
    ).toBeInTheDocument();

    const connect = within(dialog).getByRole("button", {
      name: /开始探索/,
    });
    expect(connect).toBeEnabled();
    await user.click(connect);
    expect(onNext).toHaveBeenCalledTimes(1);
    expect(onNext).toHaveBeenCalledWith(rt);
  });

});
