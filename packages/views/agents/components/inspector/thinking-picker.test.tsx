// @vitest-environment jsdom

import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { RuntimeModelThinkingLevel } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/zh-Hans/common.json";
import enAgents from "../../../locales/zh-Hans/agents.json";
import enIssues from "../../../locales/zh-Hans/issues.json";

import { ThinkingPicker } from "./thinking-picker";

const TEST_RESOURCES = {
  "zh-Hans": { common: enCommon, agents: enAgents, issues: enIssues },
};

const CODEX_LEVELS: RuntimeModelThinkingLevel[] = [
  { value: "minimal", label: "Minimal", description: "Fast, light reasoning" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "高" },
];

function renderPicker(props: Partial<React.ComponentProps<typeof ThinkingPicker>> = {}) {
  const onChange = vi.fn();
  const utils = render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <ThinkingPicker
        value=""
        levels={CODEX_LEVELS}
        canEdit
        onChange={onChange}
        {...props}
      />
    </I18nProvider>,
  );
  return { ...utils, onChange };
}

describe("ThinkingPicker", () => {
  it('renders "跟随 CLI 配置" when value is empty', () => {
    renderPicker({ value: "" });
    // The trigger and the tooltip both carry the label. Empty value means
    // Multica omits --effort, so the local CLI's config decides the
    // reasoning level — see thinking-prop-row.tsx for the contract.
    expect(screen.getAllByText("跟随 CLI 配置").length).toBeGreaterThan(0);
  });

  it("renders the matching level label when value is set", () => {
    renderPicker({ value: "high" });
    expect(screen.getAllByText("高").length).toBeGreaterThan(0);
  });

  it("renders the raw token when the saved value is no longer in the catalog", () => {
    // Simulates a model swap that dropped the option the user previously
    // picked — we still surface what's persisted so the user can clear it,
    // rather than silently showing "跟随 CLI 配置".
    renderPicker({ value: "xhigh", levels: CODEX_LEVELS });
    expect(screen.getAllByText("xhigh").length).toBeGreaterThan(0);
  });

  it("renders a static read-only display when canEdit=false and exposes no popover trigger", () => {
    renderPicker({ value: "low", canEdit: false });
    expect(screen.getByText("Low")).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("calls onChange with the picked value and skips when the user re-picks the current value", async () => {
    const user = userEvent.setup();
    const { onChange } = renderPicker({ value: "low" });
    await user.click(screen.getByRole("button"));

    // Picking a new level fires onChange with the runtime-native value.
    await user.click(screen.getByText("高"));
    expect(onChange).toHaveBeenCalledWith("high");

    // Re-opening and clicking the already-selected value is a no-op so we
    // don't enqueue a redundant PATCH. The trigger also reads "Low", so
    // there are two matches in the DOM — target the listbox item by
    // selecting the option button explicitly.
    onChange.mockClear();
    await user.click(screen.getByRole("button"));
    const lowOption = screen
      .getAllByRole("button")
      .find((b) => b.getAttribute("data-picker-item") !== null && b.textContent?.includes("Low"));
    expect(lowOption).toBeDefined();
    await user.click(lowOption!);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("clears to empty string via the footer button when a value is set", async () => {
    const user = userEvent.setup();
    const { onChange } = renderPicker({ value: "high" });
    await user.click(screen.getByRole("button"));
    // Footer copy resolves through i18n — match a substring so we don't
    // pin to the exact translated wording.
    const clearButton = screen.getByTitle(/清除覆盖/i);
    await user.click(clearButton);
    expect(onChange).toHaveBeenCalledWith("");
  });

  it("does not render the clear button when value is already empty", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "" });
    await user.click(screen.getByRole("button"));
    expect(screen.queryByTitle(/Clear and fall back/i)).toBeNull();
  });
});
