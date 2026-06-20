import type { ReactNode } from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enSettings from "../../locales/zh-Hans/settings.json";

const mockUpdateWorkspace = vi.hoisted(() => vi.fn());
const mockDeleteInstallation = vi.hoisted(() => vi.fn());
const mockGetConnectURL = vi.hoisted(() => vi.fn());
const mockInvalidate = vi.hoisted(() => vi.fn());
const mockNavPush = vi.hoisted(() => vi.fn());
const mockSetQueryData = vi.hoisted(() => vi.fn());

const workspaceRef = vi.hoisted(() => ({
  current: {
    id: "workspace-1",
    name: "Acme",
    slug: "acme",
    settings: {} as Record<string, unknown>,
    repos: [{ url: "https://github.com/acme/api" }] as { url: string }[],
  },
}));
type MemberRole = "owner" | "admin" | "member" | "guest";
const membersRef = vi.hoisted(() => ({
  current: [{ user_id: "user-1", role: "owner" as MemberRole }],
}));
const installationsRef = vi.hoisted(() => ({
  current: {
    installations: [] as {
      id: string;
      account_login: string;
      installation_id?: number;
      connected_by?: string;
    }[],
    configured: true,
    can_manage: true as boolean,
  },
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey: unknown[] }) => {
    const key = JSON.stringify(opts.queryKey);
    if (key.includes("members")) return { data: membersRef.current };
    if (key.includes("installations")) return { data: installationsRef.current };
    return { data: undefined };
  },
  useQueryClient: () => ({
    setQueryData: mockSetQueryData,
    invalidateQueries: mockInvalidate,
  }),
  queryOptions: <T,>(opts: T) => opts,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => workspaceRef.current,
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
  workspaceKeys: { list: () => ["workspaces"] },
}));

vi.mock("@multica/core/github", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/github")>("@multica/core/github");
  return {
    ...actual,
    githubInstallationsOptions: () => ({
      queryKey: ["github", "installations"],
      queryFn: vi.fn(),
    }),
  };
});

vi.mock("@multica/core/api", () => ({
  api: {
    updateWorkspace: mockUpdateWorkspace,
    deleteGitHubInstallation: mockDeleteInstallation,
    getGitHubConnectURL: mockGetConnectURL,
  },
}));

vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (sel?: (s: { user: { id: string } }) => unknown) =>
      sel ? sel({ user: { id: "user-1" } }) : { user: { id: "user-1" } },
    { getState: () => ({ user: { id: "user-1" } }) },
  );
  return { useAuthStore };
});

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    push: mockNavPush,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/settings",
    searchParams: new URLSearchParams("tab=github"),
    getShareableUrl: (p: string) => `https://app.example${p}`,
  }),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { GitHubTab } from "./github-tab";

const TEST_RESOURCES = {
  "zh-Hans": { common: enCommon, settings: enSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function resetFixtures() {
  vi.clearAllMocks();
  workspaceRef.current = {
    id: "workspace-1",
    name: "Acme",
    slug: "acme",
    settings: {},
    repos: [{ url: "https://github.com/acme/api" }],
  };
  membersRef.current = [{ user_id: "user-1", role: "owner" }];
  installationsRef.current = { installations: [], configured: true, can_manage: true };
}

describe("GitHubTab", () => {
  beforeEach(resetFixtures);

  it("folds the non-dev hint into the master switch description (no separate callout)", () => {
    render(<GitHubTab />, { wrapper: I18nWrapper });
    expect(screen.getByText(/团队不使用 GitHub？在这里直接关掉即可/)).toBeTruthy();
    // The old standalone callout (title + dedicated "Turn GitHub off" button) is gone.
    expect(screen.queryByRole("button", { name: /^Turn GitHub off$/ })).toBeNull();
  });

  it("does not show the hint once the master switch is off", () => {
    workspaceRef.current.settings = { github_enabled: false };
    render(<GitHubTab />, { wrapper: I18nWrapper });
    expect(screen.queryByText(/团队不使用 GitHub？/)).toBeNull();
  });

  it("disables every feature switch when the master switch is off", () => {
    workspaceRef.current.settings = { github_enabled: false };
    render(<GitHubTab />, { wrapper: I18nWrapper });

    const master = screen.getByRole("switch", { name: /启用 GitHub 功能/i });
    expect(master.getAttribute("aria-checked")).toBe("false");

    const switches = screen.getAllByRole("switch");
    // First switch is master; remaining must be disabled (aria-disabled or disabled attr)
    const features = switches.slice(1);
    expect(features.length).toBeGreaterThan(0);
    for (const sw of features) {
      const ariaDisabled = sw.getAttribute("aria-disabled");
      const disabled = sw.hasAttribute("disabled");
      expect(ariaDisabled === "true" || disabled).toBe(true);
    }
  });

  it("flipping the master switch off persists github_enabled=false and merges existing settings", async () => {
    const user = userEvent.setup();
    workspaceRef.current.settings = { co_authored_by_enabled: true };
    mockUpdateWorkspace.mockResolvedValue({
      ...workspaceRef.current,
      settings: { co_authored_by_enabled: true, github_enabled: false },
    });

    render(<GitHubTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("switch", { name: /启用 GitHub 功能/i }));

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        settings: { co_authored_by_enabled: true, github_enabled: false },
      });
    });
  });

  it("clicking 断开 opens the confirmation and only fires on confirm", async () => {
    const user = userEvent.setup();
    installationsRef.current = {
      configured: true,
      can_manage: true,
      installations: [{ id: "inst-42", account_login: "acme", installation_id: 42 }],
    };
    mockDeleteInstallation.mockResolvedValue(undefined);

    render(<GitHubTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: /^断开$/ }));
    expect(screen.getByText(/Multica 将不再接收该安装的 webhook/i)).toBeTruthy();
    expect(mockDeleteInstallation).not.toHaveBeenCalled();

    const dialogConfirm = screen
      .getAllByRole("button", { name: /^断开$/ })
      .find((b) => b.getAttribute("data-slot")?.includes("alert-dialog"));
    await user.click(dialogConfirm ?? screen.getAllByRole("button", { name: /^断开$/ })[1]!);

    await waitFor(() => {
      expect(mockDeleteInstallation).toHaveBeenCalledWith("workspace-1", "inst-42");
    });
  });

  it("断开 button is still visible when the master switch is off", () => {
    workspaceRef.current.settings = { github_enabled: false };
    installationsRef.current = {
      configured: true,
      can_manage: true,
      installations: [{ id: "inst-1", account_login: "acme", installation_id: 1 }],
    };
    render(<GitHubTab />, { wrapper: I18nWrapper });
    expect(screen.getByRole("button", { name: /^断开$/ })).toBeTruthy();
  });

  it("non-admin sees the existing connection but no Connect/断开 controls", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    installationsRef.current = {
      configured: true,
      can_manage: false,
      installations: [{ id: "inst-1", account_login: "acme" }],
    };
    render(<GitHubTab />, { wrapper: I18nWrapper });

    expect(screen.getByText(/已连接到 acme/i)).toBeTruthy();
    expect(screen.getByText(/只读视图。/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^连接 GitHub$/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^断开$/ })).toBeNull();
  });

  it("non-admin with no connection sees the contact-admin hint", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    installationsRef.current = {
      configured: true,
      can_manage: false,
      installations: [],
    };
    render(<GitHubTab />, { wrapper: I18nWrapper });

    expect(screen.getByText(/请让管理员或所有者/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^连接 GitHub$/ })).toBeNull();
  });

  it("renders the connected_by line when the backend provides it", () => {
    installationsRef.current = {
      configured: true,
      can_manage: true,
      installations: [
        {
          id: "inst-7",
          account_login: "acme",
          installation_id: 7,
          connected_by: "Jiayuan",
        },
      ],
    };
    render(<GitHubTab />, { wrapper: I18nWrapper });
    expect(screen.getByText(/由 Jiayuan 连接/)).toBeTruthy();
  });

  it("repositories shortcut navigates to the repositories tab", async () => {
    const user = userEvent.setup();
    render(<GitHubTab />, { wrapper: I18nWrapper });
    await user.click(screen.getByRole("button", { name: /前往管理/ }));
    expect(mockNavPush).toHaveBeenCalledWith("/acme/settings?tab=repositories");
  });
});
