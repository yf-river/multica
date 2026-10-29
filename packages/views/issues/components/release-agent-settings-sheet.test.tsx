import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { ReleaseAgentSettingsSheet } from "./release-agent-settings-sheet";

const mocks = vi.hoisted(() => ({ getReleaseAgentSettings: vi.fn(), updateReleaseAgentSettings: vi.fn(), listProjects: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: mocks }));
vi.mock("@multica/core/paths", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/projects/queries", () => ({ projectListOptions: () => ({ queryKey: ["projects"], queryFn: async () => [{ id: "project-1", title: "Multica" }] }) }));
vi.mock("@multica/core/release-agent/queries", () => ({ releaseAgentKeys: { settings: () => ["release-settings"] }, releaseAgentSettingsOptions: () => ({ queryKey: ["release-settings"], queryFn: mocks.getReleaseAgentSettings }) }));
vi.mock("@multica/core/runtimes", () => ({
  runtimeListOptions: () => ({ queryKey: ["runtime"], queryFn: async () => [{ id: "runtime-1", name: "Runtime", custom_name: null, provider: "codex" }] }),
  runtimeModelsOptions: () => ({ queryKey: ["models"], queryFn: async () => ({ models: [] }) }),
  runtimeDisplayLabel: () => "Codex",
}));

describe("ReleaseAgentSettingsSheet", () => {
  beforeEach(() => {
    mocks.getReleaseAgentSettings.mockReset().mockResolvedValue({ configured: false, enabled: false, project_id: "", agent_id: "", runtime_id: "", model: "", thinking_level: "", instructions: "Default", default_instructions: "Default", can_manage: true });
    mocks.updateReleaseAgentSettings.mockReset().mockResolvedValue({});
    mocks.listProjects.mockReset().mockResolvedValue({ projects: [{ id: "project-1", title: "Multica" }] });
  });

  it("saves the selected project and runtime", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderWithI18n(<QueryClientProvider client={client}><ReleaseAgentSettingsSheet open onClose={() => {}} /></QueryClientProvider>);
    await screen.findByText("Release Agent settings");
    const project = document.getElementById("release-project") as HTMLSelectElement;
    const runtime = document.getElementById("release-runtime") as HTMLSelectElement;
    await screen.findByRole("option", { name: "Multica" });
    await screen.findByRole("option", { name: "Codex" });
    fireEvent.change(project, { target: { value: "project-1" } });
    fireEvent.change(runtime, { target: { value: "runtime-1" } });
    fireEvent.click(screen.getByText("Enable Release Agent"));
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => expect(mocks.updateReleaseAgentSettings).toHaveBeenCalledWith(expect.objectContaining({ enabled: true, project_id: "project-1", runtime_id: "runtime-1" })));
  });
});
