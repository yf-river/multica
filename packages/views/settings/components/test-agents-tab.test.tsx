import {
  screen,
  fireEvent,
  waitFor,
  within,
  act,
} from "@testing-library/react";
import { beforeEach, describe, it, expect, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";
import { api } from "@multica/core/api";
import { TestAgentsTab } from "./test-agents-tab";
vi.mock("@multica/core", () => ({ useWorkspaceId: () => "test" }));
vi.mock("@multica/core/api", () => ({
  api: { getTestAgentControl: vi.fn(), configureTestAgents: vi.fn() },
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
const policy = {
  provider: "codebuddy",
  model: "deepseek-v4.1-flash",
  thinking_level: "",
  service_tier: "",
};
const rows = [
  {
    name: "default",
    revision: 4,
    desired: { policy, overrides: {} },
    observed: {},
    observed_at: null,
  },
  {
    name: "int",
    revision: 5,
    desired: { policy, overrides: { a: policy } },
    observed: {
      revision: 5,
      status: "applied",
      agents: [
        {
          id: "a",
          name: "Agent A",
          model: policy.model,
          runtime_id: "rt-1",
          status: "idle",
          archived_at: null,
        },
      ],
      runtimes: [
        {
          id: "rt-1",
          provider: "codebuddy",
          catalog_observed_at: new Date().toISOString(),
          models: [{ id: policy.model, label: "DeepSeek Flash" }],
        },
        {
          id: "rt-2",
          provider: "codex",
          catalog_observed_at: new Date().toISOString(),
          models: [
            {
              id: "gpt-6.1-sol",
              label: "GPT-6.1 Sol",
              thinking: { supported_levels: [{ value: "low", label: "low" }] },
              supports_explicit_standard_service_tier: true,
            },
          ],
        },
      ],
    },
    observed_at: new Date().toISOString(),
  },
];
function render(
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
) {
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <TestAgentsTab />
    </QueryClientProvider>,
  );
}
beforeEach(() => vi.clearAllMocks());
describe("test environment controls", () => {
  it("applies one explicitly selected runtime and model to all environments", async () => {
    vi.spyOn(api, "getTestAgentControl").mockResolvedValue(rows as never);
    const save = vi
      .spyOn(api, "configureTestAgents")
      .mockResolvedValue({ ok: true });
    render();
    fireEvent.click(await screen.findByRole("button", { name: "Set runtime" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Runtime" }));
    fireEvent.click(await screen.findByRole("button", { name: "codex" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Model" }));
    fireEvent.click(await screen.findByRole("button", { name: "GPT-6.1 Sol" }));
    fireEvent.change(
      within(dialog).getByRole("combobox", { name: "Thinking" }),
      { target: { value: "low" } },
    );
    fireEvent.change(within(dialog).getByRole("combobox", { name: "Speed" }), {
      target: { value: "default" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Apply to all environments" }),
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
          thinking_level: "low",
          service_tier: "default",
        },
      }),
    );
    expect(
      screen.queryByRole("button", { name: "Apply" }),
    ).not.toBeInTheDocument();
  });
  it("keeps the opened configuration revision across a background refresh", async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    vi.spyOn(api, "getTestAgentControl").mockResolvedValue(rows as never);
    const save = vi
      .spyOn(api, "configureTestAgents")
      .mockResolvedValue({ ok: true });
    render(client);
    fireEvent.click(await screen.findByRole("button", { name: "Set runtime" }));
    act(() =>
      client.setQueryData(
        ["test-agent-control", "test"],
        [{ ...rows[0]!, revision: 9 }, rows[1]],
      ),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Apply to all environments" }),
    );
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith(
        expect.objectContaining({ revision: 4 }),
      ),
    );
  });
  it("does not silently replace the configured model when the catalog is missing", async () => {
    vi.spyOn(api, "getTestAgentControl").mockResolvedValue([
      rows[0],
      {
        ...rows[1],
        observed: {
          ...rows[1]!.observed,
          runtimes: [{ id: "r", provider: "codebuddy", models: [] }],
        },
      },
    ] as never);
    render();
    fireEvent.click(await screen.findByRole("button", { name: "Set runtime" }));
    expect(screen.getByRole("alert")).toHaveTextContent(
      "model catalog is unavailable",
    );
    expect(
      screen.getByRole("button", { name: "Apply to all environments" }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: "Model" })).toHaveTextContent(
      "deepseek-v4.1-flash",
    );
  });
  it("does not offer a catalog from a disabled environment", async () => {
    vi.spyOn(api, "getTestAgentControl").mockResolvedValue([
      rows[0],
      { ...rows[1], observed: { ...rows[1]!.observed, available: false } },
    ] as never);
    render();
    fireEvent.click(await screen.findByRole("button", { name: "Set runtime" }));
    expect(screen.getByRole("alert")).toHaveTextContent(
      "model catalog is unavailable",
    );
    expect(
      screen.getByRole("button", { name: "Apply to all environments" }),
    ).toBeDisabled();
  });

  it("shows access errors without a mutation control", async () => {
    vi.spyOn(api, "getTestAgentControl").mockRejectedValue(
      new Error("forbidden"),
    );
    render();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Cannot load test environments",
    );
    expect(
      screen.queryByRole("button", { name: "Set runtime" }),
    ).not.toBeInTheDocument();
  });
});
