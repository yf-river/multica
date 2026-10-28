import { screen, fireEvent, waitFor } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";
import { api } from "@multica/core/api";
import { TestAgentsTab } from "./test-agents-tab";
vi.mock("@multica/core", () => ({ useWorkspaceId: () => "test" }));
vi.mock("@multica/core/api", () => ({
  api: {
    getTestAgentControl: vi.fn(),
    configureTestAgents: vi.fn(),
    initiateListModels: vi.fn(),
  },
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
const flash = {
  provider: "codebuddy",
  model: "deepseek-v4.1-flash",
  thinking_level: "",
  service_tier: "",
};
function render() {
  return renderWithI18n(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <TestAgentsTab />
    </QueryClientProvider>,
  );
}
describe("test environment controls", () => {
  it("opens the runtime dialog and saves the chosen runtime", async () => {
    vi.spyOn(api, "getTestAgentControl").mockResolvedValue([
      {
        name: "default",
        revision: 4,
        desired: { policy: flash as never, overrides: {} },
        observed: {
          runtimes: [
            {
              id: "rt-codex",
              provider: "codex",
              models: [{ id: "gpt-6.1-sol", label: "GPT-6.1 Sol" }],
            },
            { id: "rt-codebuddy", provider: "codebuddy", models: [] },
          ],
        },
        observed_at: null,
      },
    ]);
    const save = vi
      .spyOn(api, "configureTestAgents")
      .mockResolvedValue({ ok: true });
    render();
    fireEvent.click(
      await screen.findByRole("button", { name: "Apply to all environments" }),
    );
    const runtime = await screen.findByRole("combobox", {
      name: "Test defaults runtime",
    });
    fireEvent.change(runtime, { target: { value: "codex" } });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Apply to all environments" }).at(-1)!,
    );
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith({
        action: "configure",
        name: "default",
        revision: 4,
        policy: {
          provider: "codex",
          runtime: "codex",
          model: "gpt-6.1-sol",
          thinking_level: "",
          service_tier: "",
        },
      }),
    );
  });
  it("shows a permission error without rendering mutation controls", async () => {
    vi.spyOn(api, "getTestAgentControl").mockRejectedValue(
      new Error("forbidden"),
    );
    render();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Cannot load test environments",
    );
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  });
});
