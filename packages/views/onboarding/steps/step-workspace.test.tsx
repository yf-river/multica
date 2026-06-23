import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enOnboarding from "../../locales/zh-Hans/onboarding.json";
import enWorkspace from "../../locales/zh-Hans/workspace.json";
import type { Workspace } from "@multica/core/types";

const TEST_RESOURCES = {
  "zh-Hans": {
    common: enCommon,
    onboarding: enOnboarding,
    workspace: enWorkspace,
  },
};

type MockConfigState = {
  workspaceCreationDisabled: boolean;
  daemonAppUrl: string;
};

const mockLogout = vi.hoisted(() => vi.fn());
const mockUseConfigStore = vi.hoisted(() =>
  vi.fn((selector: (state: MockConfigState) => unknown) =>
    selector({ workspaceCreationDisabled: false, daemonAppUrl: "" }),
  ),
);

vi.mock("../../auth", () => ({
  useLogout: () => mockLogout,
}));

vi.mock("@multica/core/config", () => ({
  useConfigStore: (selector: (state: MockConfigState) => unknown) =>
    mockUseConfigStore(selector),
}));

vi.mock("@multica/core/workspace/mutations", () => ({
  useCreateWorkspace: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@multica/core/api", () => ({
  api: { getBaseUrl: () => "http://127.0.0.1:8080" },
}));

import { StepWorkspace } from "./step-workspace";

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function renderStep({
  existing,
  disabled,
  daemonAppUrl = "",
}: {
  existing: Workspace | null;
  disabled: boolean;
  daemonAppUrl?: string;
}) {
  mockUseConfigStore.mockImplementation(
    (selector: (state: MockConfigState) => unknown) =>
      selector({ workspaceCreationDisabled: disabled, daemonAppUrl }),
  );
  return render(
    <StepWorkspace existing={existing} onCreated={vi.fn()} onBack={vi.fn()} />,
    { wrapper: I18nWrapper },
  );
}

const EXISTING_WORKSPACE: Workspace = {
  id: "00000000-0000-0000-0000-000000000001",
  name: "Acme",
  slug: "acme",
  description: null,
  context: null,
  settings: {},
  repos: [],
  issue_prefix: "ACM",
  created_at: "2025-01-01T00:00:00Z",
  updated_at: "2025-01-01T00:00:00Z",
} as unknown as Workspace;

// Regression for #3433 (PR feedback): when DISABLE_WORKSPACE_CREATION is on,
// every onboarding entry point must steer the user toward an existing
// workspace or a logout escape — never toward the create form, even
// indirectly (stale CTA copy, "or start another" prose, etc.).
describe("StepWorkspace — DISABLE_WORKSPACE_CREATION gate", () => {
  it("开关关闭且用户没有工作区时渲染创建表单", () => {
    renderStep({ existing: null, disabled: false });

    expect(
      screen.getByText("给工作区起个名字。", { exact: false }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("工作区名称")).toBeInTheDocument();
    expect(screen.getByLabelText("URL")).toBeInTheDocument();
  });

  it("开关开启且没有工作区时隐藏创建表单并显示禁用提示", () => {
    renderStep({ existing: null, disabled: true });

    expect(
      screen.getByText("请联系管理员为你开通账号并加入工作区。", {
        exact: false,
      }),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("工作区名称")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("URL")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /退出登录/i })).toBeInTheDocument();
  });

  it("开关开启且用户已有工作区时强制只显示现有工作区状态", () => {
    renderStep({ existing: EXISTING_WORKSPACE, disabled: true });

    // Disabled-specific copy is used in place of the "or start another" prose.
    expect(
      screen.getByText("继续使用 Acme。", { exact: false }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/重新开始/),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText(/新建一个/),
    ).not.toBeInTheDocument();

    // Resume picker still shows the existing workspace card (its name
    // appears multiple times across avatar / card / side panel — at least
    // one is enough to know the card is rendered), but the "Create a new
    // workspace" radio card is gone entirely.
    expect(screen.getAllByText("Acme").length).toBeGreaterThan(0);
    expect(
      screen.queryByText("创建一个新工作区", { exact: false }),
    ).not.toBeInTheDocument();

    // CTA is pre-selected to the existing-only action and immediately
    // enabled, so the user can press it without further interaction.
    const cta = screen.getByRole("button", { name: "打开 Acme" });
    expect(cta).toBeEnabled();
  });
});

// #4263: the workspace URL prefix must reflect the deployment's own host on
// self-hosted instances instead of the hardcoded `multica.ai`.
describe("StepWorkspace — workspace URL prefix", () => {
  it("shows the brand host when no app URL is configured", () => {
    renderStep({ existing: null, disabled: false });
    expect(screen.getByText("multica.ai/")).toBeInTheDocument();
    expect(screen.getByText("技能")).toBeInTheDocument();
    expect(screen.queryByText("Skills")).not.toBeInTheDocument();
  });

  it("shows the deployment host for self-hosted instances", () => {
    renderStep({
      existing: null,
      disabled: false,
      daemonAppUrl: "https://multica.example.com",
    });
    expect(screen.getByText("multica.example.com/")).toBeInTheDocument();
    expect(screen.queryByText("multica.ai/")).not.toBeInTheDocument();
  });
});
