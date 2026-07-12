import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api } from "@multica/core/api";
import { renderWithI18n } from "../test/i18n";
import { IssuePickerModal } from "./issue-picker-modal";

vi.mock("@multica/core/api", () => ({
  api: { searchIssues: vi.fn() },
}));

describe("IssuePickerModal", () => {
  beforeEach(() => {
    vi.mocked(api.searchIssues).mockReset();
  });

  it("shows a diagnostic error instead of an empty result when search fails", async () => {
    vi.mocked(api.searchIssues).mockRejectedValueOnce(new Error("network unavailable"));
    const user = userEvent.setup();

    renderWithI18n(
      <IssuePickerModal
        open
        onOpenChange={vi.fn()}
        title="选择任务"
        description="搜索任务"
        excludeIds={[]}
        onSelect={vi.fn()}
      />,
    );

    await user.type(screen.getByPlaceholderText("搜索任务..."), "MUL-1");
    await waitFor(() => expect(api.searchIssues).toHaveBeenCalledOnce());

    expect(await screen.findByText("搜索任务失败，请重试。")).toBeInTheDocument();
    expect(screen.queryByText("未找到任务。")).not.toBeInTheDocument();
  });
});
