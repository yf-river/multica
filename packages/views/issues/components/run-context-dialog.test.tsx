import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import issues from "../../locales/zh-Hans/issues.json";

const mocks = vi.hoisted(() => ({ query: vi.fn(), more: vi.fn(), retry: vi.fn() }));
vi.mock("@tanstack/react-query", () => ({ useInfiniteQuery: mocks.query }));
vi.mock("@multica/core/issues/queries", () => ({ taskContextOptions: () => ({}) }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace" }));

import { RunContextDialog } from "./run-context-dialog";

function showDialog() {
  return render(
    <I18nProvider locale="zh-Hans" resources={{ "zh-Hans": { issues } }}>
      <RunContextDialog issueId="issue" taskId="task" open onOpenChange={() => {}} />
    </I18nProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.query.mockReturnValue({
    data: { pages: [{ events: [{ kind: "context", input_tokens: 10 }] }] },
    isLoading: false, isError: false, hasNextPage: true, isFetchingNextPage: false,
    isFetchNextPageError: false, fetchNextPage: mocks.more, refetch: mocks.retry,
  });
});

describe("run context pagination", () => {
  it("discloses partial history and loads another page on demand", () => {
    showDialog();
    expect(screen.getByText(issues.execution_log.context_partial)).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: issues.execution_log.context_load_more }));
    expect(mocks.more).toHaveBeenCalledTimes(1);
  });

  it("offers recovery when loading fails instead of reporting an empty run", () => {
    mocks.query.mockReturnValue({ data: undefined, isLoading: false, isError: true, refetch: mocks.retry });
    showDialog();
    expect(screen.getByRole("alert")).toHaveTextContent(issues.execution_log.context_load_error);
    expect(screen.queryByText(issues.execution_log.context_empty)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: issues.execution_log.context_retry }));
    expect(mocks.retry).toHaveBeenCalledTimes(1);
  });
});
