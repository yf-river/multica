import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { mockIssue, wrapIssueActionsMenu } from "./issue-actions-test-helpers";
import { mockOpenModal } from "./issue-actions-test-mocks";

// ---------------------------------------------------------------------------
// Mocks — same pattern as the issue-detail test suite.
// ---------------------------------------------------------------------------

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: (_t: string, _id: string) => "" }),
}));

vi.mock("@multica/core/pins", () => ({
  pinListOptions: () => ({
    queryKey: ["pins", "ws-1", "user-1"],
    queryFn: () => Promise.resolve([]),
  }),
  useCreatePin: () => ({ mutate: vi.fn() }),
  useDeletePin: () => ({ mutate: vi.fn() }),
}));

vi.mock("@multica/core/issues/mutations", () => ({
  useUpdateIssue: () => ({ mutate: vi.fn() }),
}));

vi.mock("../../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorId }: any) => <span data-testid="actor">{actorId}</span>,
}));

// Import after mocks.
import { IssueActionsDropdown } from "../issue-actions-dropdown";
import { IssueActionsContextMenu } from "../issue-actions-context-menu";

beforeEach(() => {
  mockOpenModal.mockReset();
});

describe("IssueActionsDropdown", () => {
  it("renders the top-level items when the trigger is clicked", async () => {
    render(
      wrapIssueActionsMenu(
        <IssueActionsDropdown
          issue={mockIssue}
          trigger={<button data-testid="trigger">Menu</button>}
        />,
      ),
    );

    fireEvent.click(screen.getByTestId("trigger"));

    // Base UI portals the popup; role=menu lands on the popup wrapper.
    expect(await screen.findByText("状态")).toBeInTheDocument();
    expect(screen.getByText("优先级")).toBeInTheDocument();
    expect(screen.getByText("负责人")).toBeInTheDocument();
    expect(screen.getByText("截止日期")).toBeInTheDocument();
    expect(screen.getByText("复制链接")).toBeInTheDocument();
    expect(screen.getByText("更多")).toBeInTheDocument();
    expect(screen.getByText("删除任务")).toBeInTheDocument();
    // Relationship actions are hidden inside the "更多" submenu by default.
    expect(screen.queryByText("创建子任务")).not.toBeInTheDocument();
    expect(screen.queryByText("设置父任务...")).not.toBeInTheDocument();
    expect(screen.queryByText("添加子任务...")).not.toBeInTheDocument();
  });

  it("clicking the Assignee item opens the shared AssigneePicker popover", async () => {
    render(
      wrapIssueActionsMenu(
        <IssueActionsDropdown
          issue={mockIssue}
          trigger={<button data-testid="trigger">Menu</button>}
        />,
      ),
    );

    fireEvent.click(screen.getByTestId("trigger"));
    fireEvent.click(await screen.findByText("负责人"));

    // The shared picker exposes a search input and renders the workspace
    // member under a "成员" group — both come from `AssigneePicker`, not
    // the legacy submenu (which had neither).
    expect(
      await screen.findByPlaceholderText("分配给..."),
    ).toBeInTheDocument();
    expect(await screen.findByText("成员")).toBeInTheDocument();
    expect(await screen.findByText("Test User")).toBeInTheDocument();
  });

  it("clicking Delete task opens the delete-confirm modal", async () => {
    render(
      wrapIssueActionsMenu(
        <IssueActionsDropdown
          issue={mockIssue}
          trigger={<button data-testid="trigger">Menu</button>}
          onDeletedNavigateTo="/test/issues"
        />,
      ),
    );

    fireEvent.click(screen.getByTestId("trigger"));
    const del = await screen.findByText("删除任务");
    fireEvent.click(del);

    expect(mockOpenModal).toHaveBeenCalledWith("issue-delete-confirm", {
      issueId: "issue-1",
      identifier: "TES-1",
      onDeletedNavigateTo: "/test/issues",
    });
  });
});

describe("IssueActionsContextMenu", () => {
  it("renders the menu when the wrapped element receives a contextmenu event", async () => {
    render(
      wrapIssueActionsMenu(
        <IssueActionsContextMenu issue={mockIssue}>
          <div data-testid="row">Row</div>
        </IssueActionsContextMenu>,
      ),
    );

    fireEvent.contextMenu(screen.getByTestId("row"));

    expect(await screen.findByText("状态")).toBeInTheDocument();
    expect(screen.getByText("删除任务")).toBeInTheDocument();
  });

  it("anchors the shared AssigneePicker at the context-menu position", async () => {
    render(
      wrapIssueActionsMenu(
        <IssueActionsContextMenu issue={mockIssue}>
          <div data-testid="row">Row</div>
        </IssueActionsContextMenu>,
      ),
    );

    fireEvent.contextMenu(screen.getByTestId("row"), {
      clientX: 120,
      clientY: 80,
    });
    fireEvent.click(await screen.findByText("负责人"));

    expect(
      await screen.findByPlaceholderText("分配给..."),
    ).toBeInTheDocument();
    expect(await screen.findByText("Test User")).toBeInTheDocument();
  });
});
