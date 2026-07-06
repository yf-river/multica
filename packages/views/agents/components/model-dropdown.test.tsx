// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import agents from "../../locales/zh-Hans/agents.json";
import common from "../../locales/zh-Hans/common.json";
import { ModelDropdown } from "./model-dropdown";

const pickerState = vi.hoisted(() => ({
  current: {
    canCreate: false,
    models: [],
    modelsQuery: { isLoading: true, isError: false },
    search: "",
    setSearch: vi.fn(),
    supported: true,
    trimmedSearch: "",
  },
}));

vi.mock("./model-picker-state", () => ({
  useRuntimeModelPickerState: () => pickerState.current,
}));

const resources = { "zh-Hans": { agents, common } };

function renderDropdown() {
  return render(
    <I18nProvider locale="zh-Hans" resources={resources}>
      <ModelDropdown
        runtimeId="runtime-1"
        runtimeOnline
        value=""
        onChange={vi.fn()}
      />
    </I18nProvider>,
  );
}

describe("ModelDropdown", () => {
  it("shows model discovery loading state on the trigger", () => {
    renderDropdown();

    expect(screen.getByText("正在发现模型...")).toBeInTheDocument();
    expect(screen.queryByText("默认（提供方）")).not.toBeInTheDocument();
  });
});
