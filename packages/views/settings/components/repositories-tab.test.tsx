import type { ReactNode } from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enSettings from "../../locales/zh-Hans/settings.json";

const mockUpdateWorkspace = vi.hoisted(() => vi.fn());
const mockResolveWorkspaceRepo = vi.hoisted(() => vi.fn());
const mockProbeWorkspaceRepo = vi.hoisted(() => vi.fn());
const mockUpdateProjectResource = vi.hoisted(() => vi.fn());
const mockSetQueryData = vi.hoisted(() => vi.fn());
const mockInvalidateQueries = vi.hoisted(() => vi.fn());
const workspaceRef = vi.hoisted(() => ({
  current: {
    id: "workspace-1",
    name: "Test Workspace",
    slug: "test-workspace",
    repos: [
      {
        url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
        provider: "gongfeng",
        project_path: "ChainWeaver/ida/user-center",
        default_branch: "v5.0.0_dev",
        head_commit: "abc1234",
        commit_sha: "abc1234",
        connection_status: "credential_backed",
        sync_status: "synced",
        test_status: "passed",
        resolve_status: "resolved",
      },
    ],
  } as any,
}));
const projectsRef = vi.hoisted(() => ({
  current: [
    {
      id: "project-1",
      workspace_id: "workspace-1",
      title: "usercenter",
      description: null,
      icon: null,
      status: "in_progress",
      priority: "none",
      lead_type: null,
      lead_id: null,
      created_at: "2026-06-28T00:00:00Z",
      updated_at: "2026-06-28T00:00:00Z",
      issue_count: 0,
      done_count: 0,
      resource_count: 0,
    },
  ],
}));
const resourcesRef = vi.hoisted(() => ({
  current: [
    [
      {
        id: "resource-1",
        project_id: "project-1",
        workspace_id: "workspace-1",
        resource_type: "gongfeng_repo",
        label: null,
        position: 0,
        created_at: "2026-06-28T00:00:00Z",
        created_by: null,
        resource_ref: {
          provider: "gongfeng",
          url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
          project_path: "ChainWeaver/ida/user-center",
          resource_kind: "commits",
          ref: "v5.0.0_dev",
          head_commit: "abc1234",
          commit_sha: "abc1234",
          connection_status: "credential_backed",
          sync_status: "synced",
          test_status: "passed",
        },
      },
    ],
  ],
}));

vi.mock("@tanstack/react-query", () => ({
  queryOptions: (options: unknown) => options,
  useQuery: (options?: { queryKey?: readonly unknown[] }) =>
    options?.queryKey?.[0] === "projects"
      ? { data: projectsRef.current }
      : { data: [] },
  useQueries: () => resourcesRef.current.map((data) => ({ data })),
  useMutation: () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
  useQueryClient: () => ({ setQueryData: mockSetQueryData, invalidateQueries: mockInvalidateQueries }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => workspaceRef.current,
  useWorkspacePaths: () => ({
    projectDetail: (id: string) => `/test-workspace/projects/${id}`,
  }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  workspaceKeys: { list: () => ["workspaces"] },
}));

vi.mock("@multica/core/api", () => ({
  api: {
    updateWorkspace: mockUpdateWorkspace,
    resolveWorkspaceRepo: mockResolveWorkspaceRepo,
    probeWorkspaceRepo: mockProbeWorkspaceRepo,
    updateProjectResource: mockUpdateProjectResource,
  },
}));

vi.mock("@multica/core/projects", () => ({
  projectListOptions: () => ({ queryKey: ["projects"], queryFn: vi.fn() }),
  projectResourceKeys: {
    list: (_wsId: string, projectId: string) => ["project-resources", projectId],
  },
  projectResourcesOptions: (_wsId: string, projectId: string) => ({
    queryKey: ["project-resources", projectId],
    queryFn: vi.fn(),
  }),
  useSyncProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useTestProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useDeleteProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useDisableProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useEnableProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("../../navigation", () => ({
  AppLink: ({ children, href, className }: { children: ReactNode; href: string; className?: string }) => (
    <a href={href} className={className}>{children}</a>
  ),
}));

import { RepositoriesTab } from "./repositories-tab";

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

describe("RepositoriesTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    Element.prototype.scrollIntoView = vi.fn();
    workspaceRef.current = {
      id: "workspace-1",
      name: "Test Workspace",
      slug: "test-workspace",
      repos: [
        {
          url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
          provider: "gongfeng",
          project_path: "ChainWeaver/ida/user-center",
          default_branch: "v5.0.0_dev",
          head_commit: "abc1234",
          commit_sha: "abc1234",
          connection_status: "credential_backed",
          sync_status: "synced",
          test_status: "passed",
          resolve_status: "resolved",
        },
      ],
    };
    projectsRef.current = [
      {
        id: "project-1",
        workspace_id: "workspace-1",
        title: "usercenter",
        description: null,
        icon: null,
        status: "in_progress",
        priority: "none",
        lead_type: null,
        lead_id: null,
        created_at: "2026-06-28T00:00:00Z",
        updated_at: "2026-06-28T00:00:00Z",
        issue_count: 0,
        done_count: 0,
        resource_count: 0,
      },
    ] as any;
    resourcesRef.current = [
      [
        {
          id: "resource-1",
          project_id: "project-1",
          workspace_id: "workspace-1",
          resource_type: "gongfeng_repo",
          label: null,
          position: 0,
          created_at: "2026-06-28T00:00:00Z",
          created_by: null,
          resource_ref: {
            provider: "gongfeng",
            url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
            project_path: "ChainWeaver/ida/user-center",
            resource_kind: "commits",
            ref: "v5.0.0_dev",
            head_commit: "abc1234",
            commit_sha: "abc1234",
            connection_status: "credential_backed",
            sync_status: "synced",
            test_status: "passed",
          },
        },
      ],
    ];
    mockUpdateWorkspace.mockImplementation(async (_id: string, payload: { repos: { url: string }[] }) => {
      workspaceRef.current = { ...workspaceRef.current, repos: payload.repos };
      return workspaceRef.current;
    });
    const projectPathForURL = (url: string) => {
      if (url.includes("ida-deployment")) return "ChainWeaver/ida/ida-deployment";
      if (url.includes("gateway")) return "ChainWeaver/ida/gateway";
      return "ChainWeaver/ida/user-center";
    };
    mockProbeWorkspaceRepo.mockImplementation(async (_id: string, payload: { url: string }) => ({
      url: payload.url,
      provider: "gongfeng",
      project_path: projectPathForURL(payload.url),
      default_branch: "main",
      branches: ["dev_sop", "v5.0.0_dev", "main"],
      connection_status: "credential_backed",
      test_status: "passed",
    }));
    mockResolveWorkspaceRepo.mockImplementation(async (_id: string, payload: { url: string; default_branch?: string }) => ({
      url: payload.url,
      provider: "gongfeng",
      project_path: projectPathForURL(payload.url),
      default_branch: payload.default_branch || "v5.0.0_dev",
      head_commit: "def5678",
      commit_sha: "def5678",
      connection_status: "credential_backed",
      sync_status: "synced",
      test_status: "passed",
      last_tested_at: "2026-06-28T00:00:00Z",
      last_synced_at: "2026-06-28T00:00:00Z",
      resolve_status: "resolved",
      last_resolved_at: "2026-06-28T00:00:00Z",
    }));
    mockUpdateProjectResource.mockImplementation(async (_projectId: string, resourceId: string, data: any) => ({
      id: resourceId,
      ...data,
    }));
  });

  it("展示工蜂仓库资源库和项目使用情况", () => {
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    expect(screen.getByRole("heading", { name: "代码仓库" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "工蜂仓库资源库" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "工作区 Git 仓库" })).toBeNull();
    expect(screen.getByText("资源库")).toBeTruthy();
    expect(screen.getByText("默认分支：v5.0.0_dev")).toBeTruthy();
    expect(screen.getByRole("button", { name: "详情" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "删除" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "user-center" })).toHaveAttribute(
      "href",
      "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
    );
    expect(screen.getByRole("button", { name: /测试并同步工蜂仓库/ })).toBeTruthy();
    expect(screen.queryByText("项目已使用")).toBeNull();

    const rowText = screen.getByTestId("settings-gongfeng-repository-row").textContent ?? "";
    expect(rowText.indexOf("user-center")).toBeLessThan(
      rowText.indexOf("https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev"),
    );
    expect(rowText.indexOf("https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev")).toBeLessThan(
      rowText.indexOf("默认分支：v5.0.0_dev"),
    );
  });

  it("添加工蜂仓库时先快捷填充并检测分支，再按选中默认分支更新 workspace.repos", async () => {
    const user = userEvent.setup();
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "添加仓库" }));
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(screen.getByRole("button", { name: "请先检测仓库链接" })).toHaveAttribute("disabled");
    expect(screen.getByRole("button", { name: "usercenter" })).toHaveAttribute("disabled");

    await user.click(screen.getByRole("button", { name: "gateway" }));
    expect(screen.getByPlaceholderText(/git.code.tencent.com/)).toHaveValue(
      "https://git.code.tencent.com/ChainWeaver/ida/gateway",
    );
    expect(screen.getByRole("button", { name: "请先检测仓库链接" })).toHaveAttribute("disabled");
    expect(screen.getByRole("button", { name: "添加" })).toHaveAttribute("disabled");

    await user.click(screen.getByRole("button", { name: "检测" }));
    await waitFor(() => {
      expect(mockProbeWorkspaceRepo).toHaveBeenCalledWith("workspace-1", {
        url: "https://git.code.tencent.com/ChainWeaver/ida/gateway",
      });
      expect(screen.getByRole("button", { name: "dev_sop" })).not.toHaveAttribute("disabled");
    });
    expect(screen.getByText("已连接 ChainWeaver/ida/gateway，可选分支 3 个。")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "dev_sop" }));
    await user.type(screen.getByPlaceholderText("搜索分支"), "v5");
    await user.click(screen.getByRole("option", { name: "v5.0.0_dev" }));

    await user.click(screen.getByRole("button", { name: "添加" }));

    await waitFor(() => {
      expect(mockResolveWorkspaceRepo).toHaveBeenCalledWith("workspace-1", {
        url: "https://git.code.tencent.com/ChainWeaver/ida/gateway",
        default_branch: "v5.0.0_dev",
      });
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        repos: [
          {
            url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
            provider: "gongfeng",
            project_path: "ChainWeaver/ida/user-center",
            default_branch: "v5.0.0_dev",
            head_commit: "abc1234",
            commit_sha: "abc1234",
            connection_status: "credential_backed",
            sync_status: "synced",
            test_status: "passed",
            resolve_status: "resolved",
          },
          {
            url: "https://git.code.tencent.com/ChainWeaver/ida/gateway",
            provider: "gongfeng",
            project_path: "ChainWeaver/ida/gateway",
            default_branch: "v5.0.0_dev",
            head_commit: "def5678",
            commit_sha: "def5678",
            connection_status: "credential_backed",
            sync_status: "synced",
            test_status: "passed",
            last_tested_at: "2026-06-28T00:00:00Z",
            last_synced_at: "2026-06-28T00:00:00Z",
            resolve_status: "resolved",
            last_resolved_at: "2026-06-28T00:00:00Z",
          },
        ],
      });
    });
  });

  it("工蜂仓库链接改动后会清空检测结果并禁止直接添加", async () => {
    const user = userEvent.setup();
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "添加仓库" }));
    await user.click(screen.getByRole("button", { name: "gateway" }));
    await user.click(screen.getByRole("button", { name: "检测" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "dev_sop" })).toBeTruthy());

    await user.clear(screen.getByPlaceholderText(/git.code.tencent.com/));
    await user.type(screen.getByPlaceholderText(/git.code.tencent.com/), "https://git.code.tencent.com/ChainWeaver/ida/ida-deployment");

    expect(screen.getByRole("button", { name: "请先检测仓库链接" })).toHaveAttribute("disabled");
    expect(screen.getByRole("button", { name: "添加" })).toHaveAttribute("disabled");
    expect(screen.queryByText((content) => content.includes("已连接 ChainWeaver/ida/gateway"))).toBeNull();
  });

  it("详情中区分资源库信息和项目关联删除", async () => {
    const user = userEvent.setup();
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "删除" }));
    expect(mockUpdateWorkspace).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "详情" }));

    expect(screen.getByRole("dialog", { name: "工蜂仓库详情" })).toBeTruthy();
    expect(screen.getByText("默认分支")).toBeTruthy();
    expect(screen.getAllByText("v5.0.0_dev").length).toBeGreaterThan(0);
    expect(screen.getByText("Commit ID")).toBeTruthy();
    expect(screen.getByText("abc1234")).toBeTruthy();
    expect(screen.getByText("项目关联")).toBeTruthy();
    expect(screen.getByRole("button", { name: "删除项目关联" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "测试并同步项目关联" })).toBeNull();
    expect(screen.queryByRole("button", { name: "停用项目关联" })).toBeNull();
  });

  it("旧资源库数据详情用项目关联字段兜底展示默认分支和工蜂项目", async () => {
    const user = userEvent.setup();
    workspaceRef.current = {
      ...workspaceRef.current,
      repos: [
        {
          url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
        },
      ],
    };
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "详情" }));

    expect(screen.getByRole("dialog", { name: "工蜂仓库详情" })).toBeTruthy();
    expect(screen.getAllByText("v5.0.0_dev").length).toBeGreaterThan(0);
    expect(screen.getByText("ChainWeaver/ida/user-center")).toBeTruthy();
    expect(screen.getByText("Commit ID")).toBeTruthy();
    expect(screen.getByText("这条仓库是旧记录，缺少工蜂项目和默认分支等元信息。可以重新解析后补全。")).toBeTruthy();
    expect(screen.getByRole("button", { name: "补全信息" })).toBeTruthy();
  });

  it("旧资源库数据可以重新解析并回写 workspace.repos", async () => {
    const user = userEvent.setup();
    workspaceRef.current = {
      ...workspaceRef.current,
      repos: [
        {
          url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
        },
      ],
    };
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "详情" }));
    await user.click(screen.getByRole("button", { name: "补全信息" }));

    await waitFor(() => {
      expect(mockResolveWorkspaceRepo).toHaveBeenCalledWith("workspace-1", {
        url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
      });
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        repos: [
          {
            url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
            provider: "gongfeng",
            project_path: "ChainWeaver/ida/user-center",
            default_branch: "v5.0.0_dev",
            head_commit: "def5678",
            commit_sha: "def5678",
            connection_status: "credential_backed",
            sync_status: "synced",
            test_status: "passed",
            last_tested_at: "2026-06-28T00:00:00Z",
            last_synced_at: "2026-06-28T00:00:00Z",
            resolve_status: "resolved",
            last_resolved_at: "2026-06-28T00:00:00Z",
          },
        ],
      });
    });
  });

  it("不再把仅项目侧存在的 Gongfeng 资源展示成资源库行", () => {
    workspaceRef.current = { ...workspaceRef.current, repos: [] };
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    expect(screen.getByText("暂无工蜂仓库。")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "user-center" })).toBeNull();
    expect(screen.queryByRole("button", { name: "删除" })).toBeNull();
    expect(screen.queryByRole("button", { name: /测试并同步工蜂仓库/ })).toBeNull();
  });

  it("兼容旧库中 workspace.repos 为对象的脏数据", () => {
    workspaceRef.current = { ...workspaceRef.current, repos: {} as any };
    resourcesRef.current = [[]];

    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    expect(screen.getByText("暂无工蜂仓库。")).toBeTruthy();
    expect(screen.getByRole("button", { name: "添加仓库" })).toBeTruthy();
  });

  it("未绑定项目的资源库仓库主行仍显示仓库身份，详情中不提供绑定入口", async () => {
    const user = userEvent.setup();
    resourcesRef.current = [[]];
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    expect(screen.queryByText("尚未被项目使用")).toBeNull();
    expect(screen.getByRole("link", { name: "user-center" })).toHaveAttribute(
      "href",
      "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
    );
    expect(screen.getByRole("button", { name: /测试并同步工蜂仓库/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: "删除" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "详情" }));
    expect(screen.getByText("暂无项目关联")).toBeTruthy();
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(screen.queryByRole("button", { name: "绑定项目" })).toBeNull();
  });

  it("主行健康按钮按仓库资源同步，不依赖项目绑定", async () => {
    const user = userEvent.setup();
    resourcesRef.current = [[]];
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: /测试并同步工蜂仓库/ }));

    await waitFor(() => {
      expect(mockResolveWorkspaceRepo).toHaveBeenCalledWith("workspace-1", {
        url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
        default_branch: "v5.0.0_dev",
      });
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        repos: [
          {
            url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
            provider: "gongfeng",
            project_path: "ChainWeaver/ida/user-center",
            default_branch: "v5.0.0_dev",
            head_commit: "def5678",
            commit_sha: "def5678",
            connection_status: "credential_backed",
            sync_status: "synced",
            test_status: "passed",
            last_tested_at: "2026-06-28T00:00:00Z",
            last_synced_at: "2026-06-28T00:00:00Z",
            resolve_status: "resolved",
            last_resolved_at: "2026-06-28T00:00:00Z",
          },
        ],
      });
    });
  });

  it("修改默认分支时确认后同步同一工蜂项目的全部项目关联", async () => {
    const user = userEvent.setup();
    projectsRef.current = [
      ...(projectsRef.current as any[]),
      {
        id: "project-2",
        workspace_id: "workspace-1",
        title: "usercenter staging",
        description: null,
        icon: null,
        status: "in_progress",
        priority: "none",
        lead_type: null,
        lead_id: null,
        created_at: "2026-06-28T00:00:00Z",
        updated_at: "2026-06-28T00:00:00Z",
        issue_count: 0,
        done_count: 0,
        resource_count: 0,
      },
      {
        id: "project-3",
        workspace_id: "workspace-1",
        title: "gateway",
        description: null,
        icon: null,
        status: "in_progress",
        priority: "none",
        lead_type: null,
        lead_id: null,
        created_at: "2026-06-28T00:00:00Z",
        updated_at: "2026-06-28T00:00:00Z",
        issue_count: 0,
        done_count: 0,
        resource_count: 0,
      },
    ] as any;
    resourcesRef.current = [
      resourcesRef.current[0],
      [
        {
          id: "resource-2",
          project_id: "project-2",
          workspace_id: "workspace-1",
          resource_type: "gongfeng_repo",
          label: null,
          position: 0,
          created_at: "2026-06-28T00:00:00Z",
          created_by: null,
          resource_ref: {
            provider: "gongfeng",
            url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/-/tree/release",
            project_path: "ChainWeaver/ida/user-center",
            resource_kind: "branch",
            ref: "release",
            branch: "release",
            head_commit: "old222",
            commit_sha: "old222",
            connection_status: "credential_backed",
            sync_status: "synced",
            test_status: "passed",
          },
        },
      ],
      [
        {
          id: "resource-3",
          project_id: "project-3",
          workspace_id: "workspace-1",
          resource_type: "gongfeng_repo",
          label: null,
          position: 0,
          created_at: "2026-06-28T00:00:00Z",
          created_by: null,
          resource_ref: {
            provider: "gongfeng",
            url: "https://git.code.tencent.com/ChainWeaver/ida/gateway",
            project_path: "ChainWeaver/ida/gateway",
            resource_kind: "branch",
            ref: "main",
            branch: "main",
            head_commit: "old333",
            commit_sha: "old333",
            connection_status: "credential_backed",
            sync_status: "synced",
            test_status: "passed",
          },
        },
      ],
    ] as any;

    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "详情" }));
    await user.click(screen.getByRole("button", { name: "修改默认分支" }));
    await waitFor(() => {
      expect(mockProbeWorkspaceRepo).toHaveBeenCalledWith("workspace-1", {
        url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
      });
    });
    await user.click(screen.getByRole("button", { name: "v5.0.0_dev" }));
    await user.click(screen.getByRole("option", { name: "dev_sop" }));

    expect(screen.getByText("确认切换到 dev_sop？将同步 2 个项目关联。")).toBeTruthy();
    expect(screen.getAllByText("usercenter").length).toBeGreaterThan(0);
    expect(screen.getAllByText("usercenter staging").length).toBeGreaterThan(0);
    expect(screen.queryByText("gateway")).toBeNull();

    await user.click(screen.getByRole("button", { name: "确认同步" }));

    await waitFor(() => {
      expect(mockResolveWorkspaceRepo).toHaveBeenCalledWith("workspace-1", {
        url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
        default_branch: "dev_sop",
      });
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        repos: [
          expect.objectContaining({
            url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
            default_branch: "dev_sop",
            head_commit: "def5678",
            commit_sha: "def5678",
          }),
        ],
      });
      expect(mockUpdateProjectResource).toHaveBeenCalledTimes(2);
      expect(mockUpdateProjectResource).toHaveBeenCalledWith("project-1", "resource-1", {
        resource_ref: expect.objectContaining({
          project_path: "ChainWeaver/ida/user-center",
          resource_kind: "branch",
          ref: "dev_sop",
          branch: "dev_sop",
          head_commit: "def5678",
          commit_sha: "def5678",
        }),
      });
      expect(mockUpdateProjectResource).toHaveBeenCalledWith("project-2", "resource-2", {
        resource_ref: expect.objectContaining({
          project_path: "ChainWeaver/ida/user-center",
          resource_kind: "branch",
          ref: "dev_sop",
          branch: "dev_sop",
          head_commit: "def5678",
          commit_sha: "def5678",
        }),
      });
    });
  });

  it("修改默认分支解析失败时不更新资源库和项目关联", async () => {
    const user = userEvent.setup();
    mockResolveWorkspaceRepo.mockRejectedValueOnce(new Error("branch missing"));
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "详情" }));
    await user.click(screen.getByRole("button", { name: "修改默认分支" }));
    await user.click(screen.getByRole("button", { name: "v5.0.0_dev" }));
    await user.click(screen.getByRole("option", { name: "dev_sop" }));
    await user.click(screen.getByRole("button", { name: "确认同步" }));

    await waitFor(() => {
      expect(screen.getByText("branch missing")).toBeTruthy();
    });
    expect(mockUpdateWorkspace).not.toHaveBeenCalled();
    expect(mockUpdateProjectResource).not.toHaveBeenCalled();
  });

  it("删除阻塞按 project_path 判断，而不是精确 URL", async () => {
    const user = userEvent.setup();
    workspaceRef.current = {
      ...workspaceRef.current,
      repos: [
        {
          url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/-/tree/release",
          provider: "gongfeng",
          project_path: "ChainWeaver/ida/user-center",
          default_branch: "release",
        },
      ],
    };
    render(<RepositoriesTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: "删除" }));

    expect(mockUpdateWorkspace).not.toHaveBeenCalled();
  });
});
