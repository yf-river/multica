import type { ReactNode } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { CreateIssueDialog } from "./create-issue-dialog";

vi.mock("@multica/ui/components/ui/dialog", () => ({
  Dialog: ({
    children,
    onOpenChange,
  }: {
    children: ReactNode;
    onOpenChange: (open: boolean) => void;
  }) => (
    <div>
      <button type="button" onClick={() => onOpenChange(false)}>
        Dismiss shell
      </button>
      {children}
    </div>
  ),
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

vi.mock("./quick-create-issue", () => ({
  AgentCreatePanel: ({
    data,
    onClose,
  }: {
    data?: Record<string, unknown> | null;
    onClose: () => void;
  }) => (
    <div>
      <pre data-testid="agent-create-seed">{JSON.stringify(data)}</pre>
      <button type="button" onClick={onClose}>
        Close agent panel
      </button>
    </div>
  ),
}));

describe("CreateIssueDialog", () => {
  const seed = {
    prompt: "Investigate the regression",
    agent_id: "agent-1",
    squad_id: "squad-1",
    project_id: "project-1",
    status: "in_progress",
    priority: "high",
    start_date: "2026-07-02",
    due_date: "2026-07-10",
    parent_issue_id: "issue-1",
    parent_issue_identifier: "MUL-42",
  };

  it("always renders the agent flow and forwards the complete seed", () => {
    render(
      <CreateIssueDialog
        initialMode="manual"
        data={seed}
        onClose={vi.fn()}
      />,
    );

    expect(screen.getByTestId("agent-create-seed")).toHaveTextContent(
      JSON.stringify(seed),
    );
  });

  it("closes from either the dialog shell or the agent panel", () => {
    const onClose = vi.fn();
    render(
      <CreateIssueDialog
        initialMode="agent"
        data={seed}
        onClose={onClose}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Dismiss shell" }));
    fireEvent.click(screen.getByRole("button", { name: "Close agent panel" }));

    expect(onClose).toHaveBeenCalledTimes(2);
  });
});
