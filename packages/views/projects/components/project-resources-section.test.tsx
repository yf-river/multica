import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ReactNode } from "react";
import enProjects from "../../locales/zh-Hans/projects.json";
import { ProjectResourcesSection } from "./project-resources-section";

const gongfengRepoUrl = "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev";

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: [] }),
}));

vi.mock("@multica/core/projects", () => ({
  projectResourcesOptions: () => ({ queryKey: ["project-resources"] }),
  useCreateProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useDeleteProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useDisableProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useEnableProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useSyncProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useTestProjectResource: () => ({ mutateAsync: vi.fn(), isPending: false }),
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
      },
    ],
  }),
}));

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

    expect(screen.getAllByText(gongfengRepoUrl).length).toBeGreaterThan(0);
    expect(screen.getByRole("textbox", { name: "搜索仓库..." })).toBeInTheDocument();
    expect(
      screen.queryByPlaceholderText("https://git.code.tencent.com/group/project"),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "添加" })).not.toBeInTheDocument();
  });
});
