import type { ReactNode } from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import zhCommon from "../../locales/zh-Hans/common.json";
import zhSettings from "../../locales/zh-Hans/settings.json";

const navigationRef = vi.hoisted(() => ({
  current: {
    pathname: "/acme/settings",
    searchParams: new URLSearchParams(),
  },
}));
const mockReplace = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", name: "Acme", slug: "acme" }),
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    pathname: navigationRef.current.pathname,
    searchParams: navigationRef.current.searchParams,
    replace: mockReplace,
  }),
}));

vi.mock("./account-tab", () => ({
  AccountTab: () => <div>Profile content</div>,
}));
vi.mock("./preferences-tab", () => ({
  PreferencesTab: () => <div>Preferences content</div>,
}));
vi.mock("./notifications-tab", () => ({
  NotificationsTab: () => <div>Notifications content</div>,
}));
vi.mock("./tokens-tab", () => ({
  TokensTab: () => <div>Tokens content</div>,
}));
vi.mock("./workspace-tab", () => ({
  WorkspaceTab: () => <div>Workspace general content</div>,
}));
vi.mock("./repositories-tab", () => ({
  RepositoriesTab: () => <div>Repositories content</div>,
}));
vi.mock("./github-tab", () => ({
  GitHubTab: () => <div>GitHub content</div>,
}));
vi.mock("./integrations-tab", () => ({
  IntegrationsTab: () => <div>Integrations content</div>,
}));
vi.mock("./members-tab", () => ({
  MembersTab: () => <div>Members content</div>,
}));

import { SettingsPage } from "./settings-page";

const TEST_RESOURCES = {
  "zh-Hans": { common: zhCommon, settings: zhSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function renderSettingsPage(search = "") {
  navigationRef.current = {
    pathname: "/acme/settings",
    searchParams: new URLSearchParams(search),
  };
  return render(<SettingsPage />, { wrapper: I18nWrapper });
}

describe("SettingsPage tabs", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    navigationRef.current = {
      pathname: "/acme/settings",
      searchParams: new URLSearchParams(),
    };
  });

  it("does not expose the removed Labs tab", () => {
    renderSettingsPage();

    expect(screen.queryByRole("tab", { name: "实验室" })).toBeNull();
    expect(screen.queryByText("暂无实验")).toBeNull();
  });

  it("opens the GitHub settings surface used by the installation callback", () => {
    renderSettingsPage("tab=github&github_connected=1");

    expect(screen.getByRole("tab", { name: "GitHub" })).toBeTruthy();
    expect(screen.getByText("GitHub content")).toBeTruthy();
    expect(screen.queryByText("Profile content")).toBeNull();
  });

  it("keeps the legacy lark redirect on integrations", () => {
    renderSettingsPage("tab=lark");

    expect(screen.getByText("Integrations content")).toBeTruthy();
    expect(screen.queryByText("Workspace general content")).toBeNull();
  });
});
