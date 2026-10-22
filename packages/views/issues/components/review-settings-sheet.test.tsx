import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { ReviewSettingsSheet } from "./review-settings-sheet";

const mocks = vi.hoisted(() => ({
  getReviewSettings: vi.fn(),
  updateReviewSettings: vi.fn(),
  listRuntimes: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: mocks }));
vi.mock("@multica/core/paths", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/runtimes/queries", () => ({
  runtimeListOptions: () => ({
    queryKey: ["runtimes", "ws-1"],
    queryFn: async () => [
      { id: "rt-codebuddy", name: "Dev box", custom_name: null, provider: "codebuddy", status: "online" },
      { id: "rt-claude", name: "Claude box", custom_name: null, provider: "claude", status: "online" },
    ],
  }),
}));
vi.mock("@multica/core/runtimes", () => ({
  runtimeModelsOptions: () => ({
    queryKey: ["runtime-models", "rt-codebuddy"],
    queryFn: async () => ({ supported: true, models: [] }),
  }),
}));

function renderSheet() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <ReviewSettingsSheet open onClose={() => {}} />
    </QueryClientProvider>,
  );
}

describe("ReviewSettingsSheet", () => {
  beforeEach(() => {
    mocks.getReviewSettings.mockReset();
    mocks.updateReviewSettings.mockReset();
    mocks.getReviewSettings.mockResolvedValue({
      configured: false,
      enabled: false,
      agent_id: null,
      runtime_id: null,
      model: "",
      thinking_level: "",
      instructions: "默认提示词",
      instructions_customized: false,
      default_instructions: "默认提示词",
      instructions_version: "v1",
      max_rounds: 6,
      can_manage: true,
    });
    mocks.updateReviewSettings.mockResolvedValue({});
  });

  it("only offers codex/codebuddy runtimes and saves the selection", async () => {
    renderSheet();
    await screen.findByText("Review settings");
    const runtimeSelect = document.getElementById("review-runtime") as HTMLSelectElement;
    await waitFor(() => expect(runtimeSelect.disabled).toBe(false));
    expect(screen.queryByText(/Claude box/)).toBeNull();

    fireEvent.change(runtimeSelect, { target: { value: "rt-codebuddy" } });
    fireEvent.click(screen.getByText("Enable automatic review"));
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() => expect(mocks.updateReviewSettings).toHaveBeenCalled());
    expect(mocks.updateReviewSettings).toHaveBeenCalledWith(
      expect.objectContaining({ enabled: true, runtime_id: "rt-codebuddy" }),
    );
  });
});
