import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AgentRuntime } from "@multica/core/types";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import zhCommon from "../../locales/zh-Hans/common.json";
import zhOnboarding from "../../locales/zh-Hans/onboarding.json";

const TEST_RESOURCES = {
  "zh-Hans": { common: zhCommon, onboarding: zhOnboarding },
};

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
  // The CLI dialog now renders the shared runtime+model chooser, and the model
  // dropdown queries the runtime's model list.
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={qc}>
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <StepPlatformFork
        wsId="ws_test"
        onNext={onNext}
        cliInstructions={<div data-testid="cli-instructions">install me</div>}
        {...overrides}
      />
    </I18nProvider>
    </QueryClientProvider>,
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

  it("renders the three fork options at rest", () => {
    renderFork();
    expect(screen.getByText("使用这台电脑")).toBeInTheDocument();
    expect(screen.getByText("通过终端连接")).toBeInTheDocument();
    expect(screen.getByText("使用云电脑")).toBeInTheDocument();
    // Cloud option is a "Coming soon" preview — not yet wired up.
    expect(screen.getByText("即将推出")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "即将推出" }),
    ).not.toBeInTheDocument();
    // CLI dialog closed at rest → no CLI instructions.
    expect(screen.queryByTestId("cli-instructions")).not.toBeInTheDocument();
  });

  it("footer: Skip only + explanatory hint (no Continue)", () => {
    renderFork();
    expect(
      screen.getByRole("button", { name: "暂时跳过" }),
    ).toBeEnabled();
    // Continue is gone — it lived in the footer before; now advancement
    // for the CLI path is owned by the CLI dialog's own button.
    expect(
      screen.queryByRole("button", { name: "继续" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText("在上方选一种方式——或先跳过，之后再连接电脑。"),
    ).toBeInTheDocument();
  });

  it("Skip is always enabled and calls onNext(null)", async () => {
    const user = userEvent.setup();
    const { onNext } = renderFork();
    await user.click(screen.getByRole("button", { name: "暂时跳过" }));
    expect(onNext).toHaveBeenCalledTimes(1);
    expect(onNext).toHaveBeenCalledWith(null);
  });

  it("opens the download page and claims nothing about the outcome", () => {
    // mockReturnValue(null) is the honest simulation: with `noopener`,
    // window.open returns null by spec whether the tab opened or a popup
    // blocker ate it. The card used to flip to "Opened in a new tab." on
    // this exact path, which it had no way to know.
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    renderFork();

    fireEvent.click(screen.getByRole("button", { name: "下载" }));

    // Routes to the new /download page (not GitHub releases) so the
    // user lands on the OS auto-detect surface.
    expect(openSpy).toHaveBeenCalledWith(
      "/download",
      "_blank",
      "noopener,noreferrer",
    );
    // The card states its intent up front and does not change afterwards, so
    // there is no post-click claim to be wrong and no stuck "Opening…" state.
    expect(screen.getByText("使用这台电脑")).toBeInTheDocument();
  });

  it("CLI dialog: opens with instructions + 'waiting' and a disabled Connect button", async () => {
    renderFork();

    fireEvent.click(screen.getByRole("button", { name: /通过终端连接/ }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByTestId("cli-instructions")).toBeInTheDocument();
    expect(
      within(dialog).getByText(/正在等待你的电脑/),
    ).toBeInTheDocument();
    // Starting with Mika stays disabled while no runtime is selected.
    expect(
      within(dialog).getByRole("button", { name: /开始使用 Mika/ }),
    ).toBeDisabled();
  });

  it("CLI dialog with a selected runtime: Connect enables and fires onNext(runtime)", async () => {
    const rt = makeRuntime({ id: "rt_claude", name: "Claude Code" });
    resetPicker({
      runtimes: [rt],
      selected: rt,
      selectedId: rt.id,
      hasRuntimes: true,
    });
    const user = userEvent.setup();
    const { onNext } = renderFork();

    fireEvent.click(screen.getByRole("button", { name: /通过终端连接/ }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/已连接 1 台电脑/)).toBeInTheDocument();
    expect(
      within(dialog).getByText(/已选择：Claude Code/),
    ).toBeInTheDocument();

    const connect = within(dialog).getByRole("button", {
      name: /开始使用 Mika/,
    });
    expect(connect).toBeEnabled();
    await user.click(connect);
    expect(onNext).toHaveBeenCalledTimes(1);
    // The web CLI path now carries a model alongside the runtime, like the
    // desktop step does. Nothing was picked here, so it stays undefined and
    // the runtime's own default applies.
    expect(onNext).toHaveBeenCalledWith(rt, undefined);
  });

});
