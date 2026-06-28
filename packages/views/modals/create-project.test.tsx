import React from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../test/i18n";

const longRepoUrl =
  "https://git.code.tencent.com/ChainWeaver/ida/a-very-long-repository-name-that-needs-a-tooltip/commits/v5.0.0_dev";
const userCenterRepoUrl = "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev";
const gatewayRepoUrl = "https://git.code.tencent.com/ChainWeaver/ida/gateway/commits/v5.0.0_dev";

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: [] }),
}));

vi.mock("@multica/core/projects/mutations", () => ({
  useCreateProject: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("@multica/core/projects", () => ({
  useProjectDraftStore: (selector: (state: unknown) => unknown) =>
    selector({
      draft: {
        title: "",
        description: "",
        status: "planned",
        priority: "medium",
        leadType: undefined,
        leadId: undefined,
        icon: undefined,
      },
      setDraft: vi.fn(),
      clearDraft: vi.fn(),
    }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({
    id: "workspace-1",
    name: "Test Workspace",
    slug: "test-workspace",
    repos: [
      {
        url: longRepoUrl,
        project_path: "ChainWeaver/ida/a-very-long-repository-name-that-needs-a-tooltip",
      },
      {
        url: userCenterRepoUrl,
        project_path: "ChainWeaver/ida/user-center",
        default_branch: "v5.0.0_dev",
      },
      {
        url: gatewayRepoUrl,
        project_path: "ChainWeaver/ida/gateway",
        default_branch: "v5.0.0_dev",
      },
    ],
  }),
  useWorkspacePaths: () => ({
    projectDetail: (id: string) => `/test-workspace/projects/${id}`,
  }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
  agentListOptions: () => ({ queryKey: ["agents"], queryFn: vi.fn() }),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: vi.fn() }),
}));

vi.mock("../navigation", () => ({
  useNavigation: () => ({ push: vi.fn() }),
}));

vi.mock("../editor", () => {
  const ContentEditor = React.forwardRef<HTMLTextAreaElement, { placeholder?: string }>(
    ({ placeholder }, ref) => <textarea ref={ref} placeholder={placeholder} />,
  );
  ContentEditor.displayName = "ContentEditor";

  return {
    ContentEditor,
    TitleEditor: ({
      placeholder,
      onChange,
    }: {
      placeholder?: string;
      onChange?: (value: string) => void;
    }) => <input placeholder={placeholder} onChange={(e) => onChange?.(e.target.value)} />,
  };
});

vi.mock("../issues/components/priority-icon", () => ({
  PriorityIcon: () => <span data-testid="priority-icon" />,
}));

vi.mock("../common/actor-avatar", () => ({
  ActorAvatar: () => <span data-testid="actor-avatar" />,
}));

vi.mock("@multica/ui/components/ui/dialog", () => ({
  Dialog: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  DialogContent: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

vi.mock("@multica/ui/components/ui/dropdown-menu", () => ({
  DropdownMenu: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuTrigger: ({ render }: { render: React.ReactNode }) => <>{render}</>,
  DropdownMenuContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuItem: ({
    children,
    onClick,
  }: {
    children: React.ReactNode;
    onClick?: () => void;
  }) => (
    <button type="button" onClick={onClick}>
      {children}
    </button>
  ),
}));

vi.mock("@multica/ui/components/ui/popover", () => ({
  Popover: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  PopoverTrigger: ({ render }: { render: React.ReactNode }) => <>{render}</>,
  PopoverContent: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

vi.mock("@multica/ui/components/ui/tooltip", () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ render }: { render: React.ReactNode }) => <>{render}</>,
  TooltipContent: ({ children }: { children: React.ReactNode }) => (
    <div role="tooltip">{children}</div>
  ),
}));

vi.mock("@multica/ui/components/ui/button", () => ({
  Button: ({
    children,
    disabled,
    onClick,
    type = "button",
  }: {
    children: React.ReactNode;
    disabled?: boolean;
    onClick?: () => void;
    type?: "button" | "submit" | "reset";
  }) => (
    <button type={type} disabled={disabled} onClick={onClick}>
      {children}
    </button>
  ),
}));

vi.mock("@multica/ui/components/common/emoji-picker", () => ({
  EmojiPicker: () => null,
}));

vi.mock("@multica/ui/lib/utils", () => ({
  cn: (...values: Array<string | false | null | undefined>) =>
    values.filter(Boolean).join(" "),
}));

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

import { CreateProjectModal } from "./create-project";

describe("CreateProjectModal", () => {
  it("shows repository names in the picker and keeps full URLs in the tooltip", () => {
    render(<CreateProjectModal onClose={vi.fn()} />);

    expect(screen.getByTitle(`user-center · ${userCenterRepoUrl}`)).toHaveTextContent("user-center");
    expect(screen.getByTitle(`gateway · ${gatewayRepoUrl}`)).toHaveTextContent("gateway");
    expect(screen.getByRole("tooltip", { name: longRepoUrl })).toBeInTheDocument();
  });

  it("filters workspace repositories by search text", async () => {
    const user = userEvent.setup();

    renderWithI18n(<CreateProjectModal onClose={vi.fn()} />);

    const repoSearchInput = screen.getByRole("textbox", { name: "搜索仓库..." });

    await user.type(repoSearchInput, "usercenter");

    expect(
      screen.getByRole("button", { name: (name) => name.includes("user-center") }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: (name) => name.includes("gateway") }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: (name) => name.includes(longRepoUrl) }),
    ).not.toBeInTheDocument();

    await user.clear(repoSearchInput);
    await user.type(repoSearchInput, "no-match");

    expect(screen.getByText("没有匹配的仓库。")).toBeInTheDocument();
  });

  it("does not expose a custom Gongfeng URL input in the repository picker", () => {
    renderWithI18n(<CreateProjectModal onClose={vi.fn()} />);

    expect(
      screen.queryByPlaceholderText("https://git.code.tencent.com/group/project"),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "添加" })).not.toBeInTheDocument();
  });
});
