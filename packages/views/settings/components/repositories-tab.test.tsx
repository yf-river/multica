import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales-test/en/common.json";
import enSettings from "../../locales-test/en/settings.json";

const mockUpdateWorkspace = vi.hoisted(() => vi.fn());
const mockGetGitHubConnectURL = vi.hoisted(() => vi.fn());
const mockFetchNextPage = vi.hoisted(() => vi.fn());
const mockNavReplace = vi.hoisted(() => vi.fn());
const mockToastSuccess = vi.hoisted(() => vi.fn());
const workspaceRef = vi.hoisted(() => ({
  current: {
    id: "workspace-1",
    name: "Test Workspace",
    slug: "test-workspace",
    repos: [{ url: "https://github.com/multica-ai/multica" }] as {
      url: string;
      description?: string;
      provider?: string;
      default_branch?: string;
      test_status?: string;
    }[],
  },
}));
const membersRef = vi.hoisted(() => ({
  current: [{ user_id: "user-1", role: "owner" as "owner" | "admin" | "member" }],
}));
const githubRef = vi.hoisted(() => ({
  current: {
    installations: [] as { id: string; account_login: string }[],
    configured: true,
    repository_browse_configured: true,
    can_manage: true,
  },
}));
const githubQueryStateRef = vi.hoisted(() => ({
  current: {
    isPending: false,
    isFetching: false,
  },
}));
const githubRepositoriesRef = vi.hoisted(() => ({
  current: [] as {
    id: number;
    full_name: string;
    html_url: string;
    clone_url: string;
    description: string | null;
    private: boolean;
    archived: boolean;
    default_branch: string;
  }[],
}));
const searchParamsRef = vi.hoisted(() => ({
  current: new URLSearchParams("tab=repositories"),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey: readonly unknown[] }) => {
    if (options.queryKey.includes("installations")) return { data: githubRef.current, ...githubQueryStateRef.current };
    if (options.queryKey.includes("workspace-repositories")) return { data: { repos: workspaceRef.current.repos, projects: [], locations: [] }, isPending: false, isError: false, refetch: vi.fn() };
    return { data: membersRef.current };
  },
  useInfiniteQuery: () => ({
    data: {
      pages: [
        {
          repositories: githubRepositoriesRef.current,
          total_count: githubRepositoriesRef.current.length,
          next_page: null,
        },
      ],
    },
    isPending: false,
    isError: false,
    hasNextPage: false,
    isFetchingNextPage: false,
    fetchNextPage: mockFetchNextPage,
  }),
  useQueryClient: () => ({ setQueryData: vi.fn() }),
  queryOptions: <T,>(options: T) => options,
  infiniteQueryOptions: <T,>(options: T) => options,
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

vi.mock("@multica/core/api", () => ({
  api: {
    updateWorkspace: mockUpdateWorkspace,
    getGitHubConnectURL: mockGetGitHubConnectURL,
    getWorkspaceRepositoryCenter: vi.fn(async () => ({ repos: workspaceRef.current.repos, projects: [], locations: [] })),
    probeWorkspaceRepo: vi.fn(), resolveWorkspaceRepo: vi.fn(), createProjectResource: vi.fn(), deleteProjectResource: vi.fn(),
  },
}));

vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (selector?: (state: { user: { id: string } }) => unknown) =>
      selector ? selector({ user: { id: "user-1" } }) : { user: { id: "user-1" } },
    { getState: () => ({ user: { id: "user-1" } }) },
  );
  return { useAuthStore };
});

vi.mock("sonner", () => ({
  toast: { success: mockToastSuccess, error: vi.fn() },
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    push: vi.fn(),
    replace: mockNavReplace,
    back: vi.fn(),
    pathname: "/acme/settings",
    searchParams: searchParamsRef.current,
    hash: "",
    getShareableUrl: (path: string) => `https://app.example${path}`,
  }),
}));

import { RepositoriesTab, repositoryIdentity } from "./repositories-tab";

const TEST_RESOURCES = {
  en: { common: enCommon, settings: enSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}


describe("RepositoriesTab repository center", () => {
 beforeEach(() => { vi.clearAllMocks(); workspaceRef.current = { id:"workspace-1", name:"Test Workspace", slug:"test-workspace", repos:[{url:"https://github.com/multica-ai/multica", provider:"github", default_branch:"main", test_status:"passed"}] }; membersRef.current=[{user_id:"user-1",role:"owner"}]; });
 it("renders provider connections and the structured repository center", () => { render(<RepositoriesTab />, {wrapper:I18nWrapper}); expect(screen.getAllByText("GitHub").length).toBeGreaterThan(0); expect(screen.getByText("腾讯工蜂")).toBeTruthy(); expect(screen.getByText("代码来源")).toBeTruthy(); expect(screen.getByText("添加仓库")).toBeTruthy(); });
 it("supports mixed provider data without crashing", () => { workspaceRef.current.repos=[{url:"https://github.com/a/one",provider:"github",test_status:"passed"},{url:"https://git.code.tencent.com/a/two",provider:"gongfeng",test_status:"failed"}]; render(<RepositoriesTab />, {wrapper:I18nWrapper}); expect(screen.getByText("代码来源")).toBeTruthy(); });
 it("opens the four-section detail drawer", async () => { const user=userEvent.setup(); render(<RepositoriesTab />, {wrapper:I18nWrapper}); await user.click(screen.getByRole("button", {name:/查看.*详情/})); expect(screen.getByText("概览")).toBeTruthy(); expect(screen.getByText("项目绑定")).toBeTruthy(); expect(screen.getByText("运行位置")).toBeTruthy(); });
 it("keeps mutation controls hidden for ordinary members", () => { membersRef.current=[{user_id:"user-1",role:"member"}]; render(<RepositoriesTab />, {wrapper:I18nWrapper}); expect(screen.queryByRole("button", {name:"添加仓库"})).toBeNull(); });
 it("normalizes HTTPS and SSH identities", () => { expect(repositoryIdentity("https://github.com/a/one.git")).toBe("github.com/a/one"); expect(repositoryIdentity("git@github.com:a/one.git")).toBe("github.com/a/one"); });
});
