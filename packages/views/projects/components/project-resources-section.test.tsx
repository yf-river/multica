import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ReactNode } from "react";
import enProjects from "../../locales/zh-Hans/projects.json";
import { ProjectResourcesSection } from "./project-resources-section";

const gongfengRepoUrl = "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev";
const mockCreateProjectResource = vi.hoisted(() => vi.fn());
const mockProjectResources = vi.hoisted(() => vi.fn<() => unknown[]>(() => []));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: mockProjectResources() }),
}));

vi.mock("@multica/core/projects", () => ({
  projectResourcesOptions: () => ({ queryKey: ["project-resources"] }),
  useCreateProjectResource: () => ({ mutateAsync: mockCreateProjectResource, isPending: false }),
  useDeleteProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useSyncProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useUpdateProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({
    id: "workspace-1",
    repos: [
      {
        url: gongfengRepoUrl,
        provider: "gongfeng",
        project_path: "ChainWeaver/ida/user-center",
        default_branch: "v5.0.0_dev",
        head_commit: "b3c284c308ee",
        commit_sha: "b3c284c308ee",
        connection_status: "credential_backed",
        sync_status: "synced",
        test_status: "passed",
        last_tested_at: "2026-07-01T10:00:00Z",
        last_synced_at: "2026-07-01T10:01:00Z",
      },
    ],
  }),
}));

beforeEach(() => {
  mockCreateProjectResource.mockReset();
  mockCreateProjectResource.mockResolvedValue({ id: "resource-1" });
  mockProjectResources.mockReset();
  mockProjectResources.mockReturnValue([]);
});

vi.mock("@multica/ui/components/ui/popover", () => ({
  Popover: ({ children }: { children: ReactNode }) => <>{children}</>,
  PopoverTrigger: ({ render }: { render: ReactNode }) => <>{render}</>,
  PopoverContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

vi.mock("@multica/ui/components/ui/tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ render }: { render: ReactNode }) => <>{render}</>,
  TooltipContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

vi.mock("@multica/ui/components/ui/button", () => ({
  Button: ({
    children,
    disabled,
    onClick,
  type = "button",
  }: {
    children: ReactNode;
    disabled?: boolean;
    onClick?: () => void;
    type?: "button" | "submit" | "reset";
  }) => (
    <button type={type} disabled={disabled} onClick={onClick}>
      {children}
    </button>
  ),
}));

vi.mock("../../platform", () => ({
  isDesktopShell: () => false,
  useLocalDaemonStatus: () => ({ daemonId: null, running: false }),
  pickDirectory: vi.fn(),
  validateLocalDirectory: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

function renderSection() {
  return render(
    <I18nProvider locale="zh-Hans" resources={{ "zh-Hans": { projects: enProjects } }}>
      <ProjectResourcesSection projectId="project-1" />
    </I18nProvider>,
  );
}

describe("ProjectResourcesSection", () => {
  it("does not expose a custom Gongfeng URL input when adding resources", () => {
    renderSection();

    expect(screen.getByText("user-center")).toBeInTheDocument();
    expect(screen.getByText("ChainWeaver/ida/user-center")).toBeInTheDocument();
    expect(screen.getAllByText(gongfengRepoUrl).length).toBeGreaterThan(0);
    expect(screen.getByRole("textbox", { name: "搜索仓库..." })).toBeInTheDocument();
    expect(
      screen.queryByPlaceholderText("https://git.code.tencent.com/group/project"),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "添加" })).not.toBeInTheDocument();
  });

  it("attaches resolved Gongfeng metadata from the workspace repo", async () => {
    renderSection();

    fireEvent.click(screen.getByText("user-center").closest("button")!);

    await waitFor(() => {
      expect(mockCreateProjectResource).toHaveBeenCalledWith({
        resource_type: "gongfeng_repo",
        resource_ref: expect.objectContaining({
          url: gongfengRepoUrl,
          provider: "gongfeng",
          project_path: "ChainWeaver/ida/user-center",
          resource_kind: "branch",
          ref: "v5.0.0_dev",
          branch: "v5.0.0_dev",
          head_commit: "b3c284c308ee",
          commit_sha: "b3c284c308ee",
          connection_status: "credential_backed",
          sync_status: "synced",
          test_status: "passed",
          last_tested_at: "2026-07-01T10:00:00Z",
          last_synced_at: "2026-07-01T10:01:00Z",
        }),
      });
    });
  });

  it("only exposes sync and remove actions for attached Gongfeng resources", () => {
    mockProjectResources.mockReturnValue([
      {
        id: "resource-1",
        project_id: "project-1",
        workspace_id: "workspace-1",
        resource_type: "gongfeng_repo",
        resource_ref: {
          url: gongfengRepoUrl,
          provider: "gongfeng",
          project_path: "ChainWeaver/ida/user-center",
          resource_kind: "branch",
          ref: "v5.0.0_dev",
          branch: "v5.0.0_dev",
          commit_sha: "b3c284c308ee",
          connection_status: "credential_backed",
          sync_status: "synced",
          test_status: "passed",
        },
        label: null,
        position: 0,
        created_at: "2026-07-01T10:00:00Z",
        updated_at: "2026-07-01T10:00:00Z",
      },
    ]);

    renderSection();

    expect(screen.getByRole("button", { name: "同步状态" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "移除" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "测试连接" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "停用" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "启用" })).not.toBeInTheDocument();
  });
});
