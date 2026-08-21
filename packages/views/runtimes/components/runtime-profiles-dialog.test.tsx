// @vitest-environment jsdom

import { describe, expect, it, beforeEach, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { RuntimeProfile } from "@multica/core/types";
import enCommon from "../../locales/zh-Hans/common.json";
import enRuntimes from "../../locales/zh-Hans/runtimes.json";

const queryState = vi.hoisted(() => ({
  profiles: [] as RuntimeProfile[],
  isLoading: false,
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>(
      "@tanstack/react-query",
    );
  return {
    ...actual,
    useQuery: vi.fn(() => ({
      data: queryState.profiles,
      isLoading: queryState.isLoading,
    })),
  };
});

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("./delete-runtime-profile-dialog", () => ({
  DeleteRuntimeProfileDialog: () => null,
}));

vi.mock("./provider-logo", () => ({
  ProviderLogo: () => null,
}));

import { RuntimeProfilesDialog } from "./runtime-profiles-dialog";

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, runtimes: enRuntimes } };

function profile(overrides: Partial<RuntimeProfile> = {}): RuntimeProfile {
  return {
    id: "prof-1",
    workspace_id: "ws-1",
    display_name: "Team Codex",
    protocol_family: "codex",
    command_name: "codex",
    description: null,
    fixed_args: [],
    visibility: "workspace",
    created_by: "user-1",
    enabled: true,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-02T00:00:00Z",
    ...overrides,
  };
}

function renderDialog() {
  return render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <RuntimeProfilesDialog wsId="ws-1" onClose={vi.fn()} />
    </I18nProvider>,
  );
}

describe("RuntimeProfilesDialog", () => {
  beforeEach(() => {
    queryState.profiles = [];
    queryState.isLoading = false;
    vi.clearAllMocks();
  });

  it("显示自定义空态，并保持内置协议折叠", () => {
    renderDialog();

    expect(
      screen.getByText("创建第一个自定义运行时"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/选择一个基础协议族/),
    ).toBeInTheDocument();

    const builtinsToggle = screen.getByRole("button", {
      name: /支持的基础协议/,
    });
    expect(builtinsToggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("claude")).not.toBeInTheDocument();
    expect(
      screen.getAllByRole("button", { name: "新建自定义运行时" }),
    ).toHaveLength(2);
  });

  it("在折叠的内置参考区之前渲染自定义 profile", () => {
    queryState.profiles = [profile()];

    renderDialog();

    const customTitle = screen.getByText("自定义运行时（1）");
    const customRow = screen.getByText("Team Codex");
    const builtinsToggle = screen.getByRole("button", {
      name: /支持的基础协议/,
    });

    expect(customRow).toBeInTheDocument();
    expect(builtinsToggle).toHaveAttribute("aria-expanded", "false");
    expect(
      customTitle.compareDocumentPosition(builtinsToggle) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(screen.queryByText("claude")).not.toBeInTheDocument();

    fireEvent.click(builtinsToggle);

    expect(builtinsToggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("claude")).toBeInTheDocument();
  });

  it("内置参考区折叠时清空内置详情", () => {
    queryState.profiles = [profile()];

    renderDialog();

    const builtinsToggle = screen.getByRole("button", {
      name: /支持的基础协议/,
    });
    fireEvent.click(builtinsToggle);
    fireEvent.click(screen.getByRole("option", { name: /claude/i }));

    expect(
      screen.getByText(/claude 是内置协议类型/),
    ).toBeInTheDocument();

    fireEvent.click(builtinsToggle);

    expect(screen.getByText("选择一个运行时")).toBeInTheDocument();
    expect(
      screen.queryByText(/claude 是内置协议类型/),
    ).not.toBeInTheDocument();
  });
});
