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
  it("lets the operator pick a runtime and model, then saves both", async () => {
    vi.spyOn(api, "getTestAgentControl").mockResolvedValue([
      {
        name: "default",
        revision: 4,
        desired: { policy: flash as never, overrides: {} },
        observed: {
          runtimes: [
            { id: "rt-codebuddy", provider: "codebuddy" },
            { id: "rt-codex", provider: "codex" },
          ],
        },
        observed_at: null,
      },
    ]);
    vi.spyOn(api, "initiateListModels").mockResolvedValue({
      runtime_id: "rt-codex",
      models: [
        { id: "gpt-6.1-sol", label: "GPT-6.1 Sol", default: true },
        { id: "gpt-6.2-sol", label: "GPT-6.2 Sol" },
      ],
      unavailable_models: [],
    } as never);
    const save = vi
      .spyOn(api, "configureTestAgents")
      .mockResolvedValue({ ok: true });
    render();
    const runtime = await screen.findByRole("combobox", {
      name: "Test defaults runtime",
    });
    fireEvent.change(runtime, { target: { value: "rt-codex" } });
    const model = await screen.findByRole("combobox", {
      name: "Test defaults model",
    });
    await waitFor(() => expect(api.initiateListModels).toHaveBeenCalled());
    fireEvent.change(model, { target: { value: "gpt-6.2-sol" } });
    fireEvent.click(
      screen.getByRole("button", { name: "Apply to all environments" }),
    );
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith({
        action: "configure",
        name: "default",
        revision: 4,
        policy: {
          provider: "codex",
          runtime: "codex",
          model: "gpt-6.2-sol",
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
