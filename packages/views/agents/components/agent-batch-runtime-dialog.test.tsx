import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enAgents from "../../locales-test/en/agents.json";
import enCommon from "../../locales-test/en/common.json";
import { AgentBatchRuntimeDialog } from "./agent-batch-runtime-dialog";
import type { AgentListRow } from "./agents-page";
import type { AgentRuntime } from "@multica/core/types";

const update = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@multica/core/api", () => ({ api: { updateAgent: update } }));
vi.mock("@multica/core/runtimes", () => ({
  isRuntimeUsableForUser: () => true,
  runtimeModelsOptions: (id: string) => ({ queryKey: ["models", id], enabled: !!id, queryFn: async () => ({ supported: true, models: [{ id: "new-model", label: "Model", thinking: { supported_levels: [{ value: "high", label: "High" }] }, service_tiers: [{ id: "priority", name: "Fast" }], supports_explicit_standard_service_tier: true }] }) }),
}));
vi.mock("./inspector/runtime-picker", () => ({ RuntimePicker: ({ onChange }: { onChange: (id: string) => void }) => <button onClick={() => onChange("new-runtime")}>Choose runtime</button> }));
vi.mock("./inspector/model-picker", () => ({ ModelPicker: ({ onChange }: { onChange: (id: string) => void }) => <button onClick={() => onChange("new-model")}>Choose model</button> }));
const rows = ["a", "b"].map((id) => ({ agent: { id, name: id, runtime_id: `old-${id}`, model: "old-model", thinking_level: "ultra", service_tier: "priority" }, canManage: true })) as AgentListRow[];
function setup(targets = rows) {
  const success = vi.fn();
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={qc}><I18nProvider locale="en" resources={{ en: { agents: enAgents, common: enCommon } }}><AgentBatchRuntimeDialog rows={targets} runtimes={[{ id: "new-runtime", provider: "codex", status: "online" } as AgentRuntime]} members={[]} currentUserId="user" onClose={vi.fn()} onSuccess={success} /></I18nProvider></QueryClientProvider>);
  return { success, qc };
}
describe("AgentBatchRuntimeDialog", () => {
  it("saves four settings together and retries only failed agents with the same settings", async () => {
    update.mockReset().mockImplementation(async (id, data) => { if (id === "b") throw new Error("Denied"); return { id, ...data }; });
    const { success, qc } = setup();
    expect(screen.getByRole("button", { name: "Apply to 2 agents" })).toBeDisabled();
    fireEvent.click(screen.getByText("Choose runtime"));
    fireEvent.click(screen.getByText("Choose model"));
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Fast mode" })).toBeEnabled());
    fireEvent.change(screen.getByRole("combobox", { name: "Fast mode" }), { target: { value: "priority" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply to 2 agents" }));
    await screen.findByText("1 succeeded, 1 failed.");
    expect(update).toHaveBeenCalledWith("a", { runtime_id: "new-runtime", model: "new-model", thinking_level: "", service_tier: "priority" });
    expect(qc.getQueryData(["workspaces", "ws", "agents", "detail", "a"])).toBeDefined();
    update.mockClear().mockImplementation(async (id, data) => ({ id, ...data }));
    fireEvent.click(screen.getByRole("button", { name: "Retry failed" }));
    await waitFor(() => expect(success).toHaveBeenCalledOnce());
    expect(update).toHaveBeenCalledTimes(1);
    expect(update.mock.calls[0]?.[0]).toBe("b");
  });
  it("names nonmanageable targets without silently excluding them", () => {
    setup([{ ...rows[0]!, canManage: false }]);
    expect(screen.getByText(/Remove agents without management access/)).toHaveTextContent("a");
    expect(screen.getByRole("button", { name: "Apply to 1 agents" })).toBeDisabled();
  });
});
