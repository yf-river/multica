import { screen, fireEvent, waitFor } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";
import { api } from "@multica/core/api";
import { TestAgentsTab } from "./test-agents-tab";
vi.mock("@multica/core", () => ({ useWorkspaceId: () => "test" }));
vi.mock("@multica/core/api", () => ({
  api: { getTestAgentControl: vi.fn(), configureTestAgents: vi.fn() },
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
  it("submits the selected default with its revision, without a target URL", async () => {
    vi.spyOn(api, "getTestAgentControl").mockResolvedValue([
      {
        name: "default",
        revision: 4,
        desired: { policy: flash as never, overrides: {} },
        observed: {},
        observed_at: null,
      },
    ]);
    const save = vi
      .spyOn(api, "configureTestAgents")
      .mockResolvedValue({ ok: true });
    render();
    const select = await screen.findByRole("combobox", {
      name: "Test defaults",
    });
    fireEvent.change(select, { target: { value: "codex" } });
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
          model: "gpt-6.1-sol",
          thinking_level: "low",
          service_tier: "default",
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
