// @vitest-environment jsdom

import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useHealth, type HealthObservation } from "@multica/core/health";
import { renderWithI18n } from "../test/i18n";
import { ObservationEditor } from "./health-editors";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function healthQueries() {
  return {
    observation: { isPending: false, error: null, mutateAsync: vi.fn() },
    observations: { isPending: false, error: null, mutateAsync: vi.fn() },
  } as unknown as ReturnType<typeof useHealth>;
}

describe("ObservationEditor", () => {
  it("starts a new measurement empty while showing the previous value", () => {
    renderWithI18n(
      <ObservationEditor
        value={{ value: "98.8", unit: "kg", observedAt: "2025-07-26" }}
        preset="weight"
        mode="update"
        revision={7}
        h={healthQueries()}
        onClose={vi.fn()}
      />,
      { locale: "zh-Hans" },
    );

    expect(screen.getByRole("heading", { name: "记录体重" })).toBeVisible();
    expect(screen.getByText(/上次记录：98.8 kg/)).toBeVisible();
    expect(screen.getByRole("spinbutton", { name: /^测量结果/ })).toHaveValue(null);
    expect(screen.queryByText("更多信息")).not.toBeInTheDocument();
  });

  it("prefills the original value only when correcting a record", () => {
    renderWithI18n(
      <ObservationEditor
        value={{ value: "98.8", unit: "kg", observedAt: "2025-07-26" }}
        preset="weight"
        mode="correct"
        revision={7}
        h={healthQueries()}
        onClose={vi.fn()}
      />,
      { locale: "zh-Hans" },
    );

    expect(screen.getByRole("heading", { name: "修正记录" })).toBeVisible();
    expect(screen.getByRole("spinbutton", { name: /^测量结果/ })).toHaveValue(98.8);
    expect(screen.getByDisplayValue("2025-07-26")).toBeVisible();
  });

  it("shows the latest value after choosing a metric from the global recorder", () => {
    renderWithI18n(
      <ObservationEditor
        value={{}}
        recent={[{ id: "weight-1", name: "体重", code: "weight", category: "一般检查", value: "98.8", unit: "kg", observedAt: "2025-07-26", source: "manual", status: "accepted" } as HealthObservation]}
        mode="new"
        revision={7}
        h={healthQueries()}
        onClose={vi.fn()}
      />,
      { locale: "zh-Hans" },
    );

    fireEvent.click(screen.getByRole("button", { name: "体重" }));
    expect(screen.getByText(/上次记录：98.8 kg/)).toBeVisible();
    expect(screen.getByRole("spinbutton", { name: /^测量结果/ })).toHaveValue(null);
    expect(screen.queryByRole("button", { name: "其他指标" })).not.toBeInTheDocument();
  });

  it("previews BMI without writing a measurement", () => {
    const h = healthQueries();
    renderWithI18n(<ObservationEditor value={{}} recent={[{ id: "h", code: "height", value: "180", unit: "cm" } as HealthObservation]} preset="weight" mode="new" revision={7} h={h} onClose={vi.fn()} />, { locale: "zh-Hans" });
    fireEvent.change(screen.getByRole("spinbutton", { name: /^测量结果/ }), { target: { value: "90" } });
    expect(screen.getByText("27.8")).toBeVisible();
    expect(h.observation.mutateAsync).not.toHaveBeenCalled();
  });
});
