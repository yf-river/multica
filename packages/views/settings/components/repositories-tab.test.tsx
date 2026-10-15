import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales-test/en/common.json";
import zhSettings from "../../locales/zh-Hans/settings.json";

const mockCreateRepository = vi.hoisted(() => vi.fn());
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
    if (options.queryKey.includes("external-credential-profiles")) return {data: [], isPending:false};
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
  useQueryClient: () => ({ setQueryData: vi.fn(), invalidateQueries: vi.fn() }),
  useMutation: (options: {mutationFn: (input: unknown) => Promise<unknown>; onSuccess?: (data: unknown) => void}) => ({mutateAsync: async (input: unknown) => {const data=await options.mutationFn(input); options.onSuccess?.(data); return data;}}),
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
    createRepository: mockCreateRepository,
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

import { RepositoriesTab, repositoryIdentity, repositoryStatus } from "./repositories-tab";

const TEST_RESOURCES = {
  en: { common: enCommon, settings: zhSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}


describe("RepositoriesTab repository center", () => {
 beforeEach(() => { vi.clearAllMocks(); githubRef.current.installations=[]; githubRepositoriesRef.current=[]; mockCreateRepository.mockImplementation(async (_id, input) => ({...workspaceRef.current, repos:[input]})); workspaceRef.current = { id:"workspace-1", name:"Test Workspace", slug:"test-workspace", repos:[{url:"https://github.com/multica-ai/multica", provider:"github", default_branch:"main", test_status:"passed"}] }; membersRef.current=[{user_id:"user-1",role:"owner"}]; });
 it("renders provider connections and the structured repository center", () => { render(<RepositoriesTab />, {wrapper:I18nWrapper}); expect(screen.getAllByText("GitHub").length).toBeGreaterThan(0); expect(screen.getByText("腾讯工蜂")).toBeTruthy(); expect(screen.getByText("Code repositories")).toBeTruthy(); expect(screen.getByText("添加仓库")).toBeTruthy(); });
 it("supports mixed provider data without crashing", () => { workspaceRef.current.repos=[{url:"https://github.com/a/one",provider:"github",test_status:"passed"},{url:"https://git.code.tencent.com/a/two",provider:"gongfeng",test_status:"failed"}]; render(<RepositoriesTab />, {wrapper:I18nWrapper}); expect(screen.getByText("代码仓库")).toBeTruthy(); });
 it("opens the four-section detail drawer", async () => { const user=userEvent.setup(); render(<RepositoriesTab />, {wrapper:I18nWrapper}); await user.click(screen.getByRole("button", {name:/查看.*详情/})); expect(screen.getByText("概览")).toBeTruthy(); expect(screen.getByText("项目绑定")).toBeTruthy(); expect(screen.getByText("运行位置")).toBeTruthy(); });
 it("keeps mutation controls hidden for ordinary members", () => { membersRef.current=[{user_id:"user-1",role:"member"}]; render(<RepositoriesTab />, {wrapper:I18nWrapper}); expect(screen.queryByRole("button", {name:"添加仓库"})).toBeNull(); });
 it("normalizes HTTPS and SSH identities", () => { expect(repositoryIdentity("https://github.com/a/one.git")).toBe("github.com/a/one"); expect(repositoryIdentity("git@github.com:a/one.git")).toBe("github.com/a/one"); });
 it("renders source and status options when opening the filters", async () => {
   const user=userEvent.setup(); render(<RepositoriesTab />, {wrapper:I18nWrapper});
   await user.click(screen.getByRole("combobox",{name:"来源筛选"}));
   expect(screen.getByRole("option",{name:"其他 Git"})).toBeTruthy();
   await user.click(screen.getByRole("option",{name:"GitHub"}));
   await user.click(screen.getByRole("combobox",{name:"状态筛选"}));
   expect(screen.getByRole("option",{name:"连接异常"})).toBeTruthy();
 });
 it("reports verified backend repository states correctly", () => {
   expect(repositoryStatus({url:"x",test_status:"passed",connection_status:"credential_backed"})).toBe("connected");
   expect(repositoryStatus({url:"x",test_status:"passed",sync_status:"failed"})).toBe("failed");
 });
 it("does not show an endless branch loader to an ordinary member", async () => {
   membersRef.current=[{user_id:"user-1",role:"member"}]; githubRef.current.installations=[{id:"1",account_login:"acme"}];
   const user=userEvent.setup();render(<RepositoriesTab />, {wrapper:I18nWrapper});
   await user.click(screen.getByRole("button",{name:/查看.*详情/}));await user.click(screen.getByRole("tab",{name:"分支"}));
   expect(screen.getByText("当前账号没有读取分支的管理权限。")).toBeTruthy();
   expect(screen.queryByText("正在读取分支")).toBeNull();
 });
 it("preserves GitHub provider and default branch and prevents duplicate imports", async () => {
   githubRef.current.installations=[{id:"1",account_login:"acme"}];
   githubRepositoriesRef.current=[{id:42,full_name:"acme/new",clone_url:"https://github.com/acme/new.git",html_url:"https://github.com/acme/new",description:null,private:false,archived:false,default_branch:"develop"}];
   let complete: (value: unknown) => void = () => {};
   mockCreateRepository.mockImplementation(()=>new Promise(resolve=>{complete=resolve;}));
   const user=userEvent.setup();render(<RepositoriesTab />, {wrapper:I18nWrapper});
   await user.click(screen.getByRole("button",{name:"管理GitHub"}));
   await user.click(screen.getByRole("checkbox"));
   await user.click(screen.getByRole("button",{name:"添加仓库"}));
   expect(mockCreateRepository).toHaveBeenCalledTimes(1);
   expect(mockCreateRepository.mock.calls[0]?.[1]).toMatchObject({provider:"github",default_branch:"develop"});
   expect(screen.getByRole("button",{name:"添加仓库"})).toBeDisabled();
   complete({...workspaceRef.current,repos:mockCreateRepository.mock.calls[0]?.[1].repos});
   await waitFor(()=>expect(screen.queryByRole("dialog")).toBeNull());
 });

 it("keeps the detail drawer on the same repository after a refreshed list is reordered", async () => {
   workspaceRef.current.repos=[{url:"https://github.com/acme/one"},{url:"https://github.com/acme/two"}];
   const user=userEvent.setup();const view=render(<RepositoriesTab />, {wrapper:I18nWrapper});
   await user.click(screen.getByRole("button",{name:"查看one详情"}));
   workspaceRef.current.repos=[...workspaceRef.current.repos].reverse();view.rerender(<RepositoriesTab />);
   expect(within(screen.getByRole("dialog")).getByText("https://github.com/acme/one",{selector:'h2'})).toBeTruthy();
 });

});
