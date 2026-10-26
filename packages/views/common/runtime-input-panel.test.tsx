import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, setApiInstance } from "@multica/core/api";
import { I18nProvider } from "@multica/core/i18n/react";
import common from "../locales/zh-Hans/common.json";
import { RuntimeInputPanel } from "./runtime-input-panel";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace" }));
const clients: QueryClient[] = [];
afterEach(() => { vi.restoreAllMocks(); for (const client of clients.splice(0)) client.clear(); });
const input = { id: "one", taskId: "task", authorId: "human", kind: "command", content: "/goal pause", status: "failed", output: "", failureReason: "delivery_unknown", createdAt: "2026-10-26T00:00:00Z" };

function show() {
  const api = new ApiClient("https://api.example.test");
  setApiInstance(api);
  const history = vi.spyOn(api, "getRuntimeInputs");
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(client);
  render(<QueryClientProvider client={client}><I18nProvider locale="zh-Hans" resources={{ "zh-Hans": { common } }}><RuntimeInputPanel taskId="task" active={false} /></I18nProvider></QueryClientProvider>);
  return history;
}

describe("runtime input history", () => {
  it("loads history only when opened, preserves unknown delivery and exposes every page", async () => {
    const history = show();
    history.mockResolvedValueOnce({ inputs: [input], nextCursor: "older", available: true })
      .mockResolvedValueOnce({ inputs: [{ ...input, id: "two", content: "older guidance", status: "delivered", output: "native confirmation", failureReason: "" }], nextCursor: "", available: true });
    expect(history).not.toHaveBeenCalled();
    fireEvent.click(screen.getByText(common.runtime_input.history));
    await waitFor(() => expect(screen.getByText(common.runtime_input.uncertain)).toBeVisible());
    fireEvent.click(screen.getByRole("button", { name: common.runtime_input.load_older }));
    await waitFor(() => expect(screen.getByText("older guidance")).toBeVisible());
    expect(history).toHaveBeenLastCalledWith("task", "older");
    expect(screen.getByText("/goal pause")).toBeVisible();
    expect(screen.getByText("native confirmation")).toBeVisible();
    expect(screen.queryByRole("button", { name: common.runtime_input.load_older })).not.toBeInTheDocument();
  });
  it("reports malformed history as a retryable read error", async () => {
    const history = show();
    history.mockResolvedValueOnce({ inputs: [], nextCursor: "", available: false });
    fireEvent.click(screen.getByText(common.runtime_input.history));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(common.runtime_input.load_error));
    expect(screen.queryByText(common.runtime_input.empty)).not.toBeInTheDocument();
    history.mockResolvedValueOnce({ inputs: [{ ...input, status: "future_state" }], nextCursor: "", available: true });
    fireEvent.click(screen.getByRole("button", { name: common.runtime_interaction.retry }));
    await waitFor(() => expect(screen.getByText(common.runtime_input.unknown)).toBeVisible());
  });
});
