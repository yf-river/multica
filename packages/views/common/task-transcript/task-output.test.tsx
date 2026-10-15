// @vitest-environment jsdom
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "@multica/core/api";
import { renderWithI18n } from "../../test/i18n";
import { TaskOutput } from "./task-output";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace" }));
vi.mock("@multica/core/api", () => ({ api: { readTaskOutput: vi.fn() } }));
afterEach(() => { cleanup(); vi.clearAllMocks(); });

it("pages and searches beyond the preview", async () => {
  vi.mocked(api.readTaskOutput).mockImplementation(async (_task, _message, offset, query) => ({
    output: query ? "tail evidence" : offset ? "second page" : "first page", offset: offset ?? 0,
    nextOffset: (offset ?? 0) + 8192, totalChars: 120000, hasMore: true, found: true,
  }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithI18n(<QueryClientProvider client={client}><TaskOutput item={{ seq: 1, type: "tool_result", id: "message", taskId: "task", outputTruncated: true }} /></QueryClientProvider>, { locale: "zh-Hans" });
  expect(await screen.findByText("first page")).toBeTruthy();
  fireEvent.click(screen.getByText("下一页"));
  expect(await screen.findByText("second page")).toBeTruthy();
  fireEvent.change(screen.getByLabelText("搜索完整输出"), { target: { value: "evidence" } });
  fireEvent.click(screen.getByText("搜索"));
  expect(await screen.findByText("tail evidence")).toBeTruthy();
  await waitFor(() => expect(api.readTaskOutput).toHaveBeenLastCalledWith("task", "message", 0, "evidence"));
});
