// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../../test/i18n";
import { SandboxSettingField } from "./sandbox-setting-field";

afterEach(cleanup);

describe("SandboxSettingField", () => {
  it("offers the three stances on a DSH runtime and submits the picked one", () => {
    const onChange = vi.fn();
    renderWithI18n(
      <SandboxSettingField
        label="Permissions"
        value="workspace-write"
        provider="dsh"
        canEdit
        onChange={onChange}
      />,
    );

    const select = screen.getByLabelText("Permissions") as HTMLSelectElement;
    expect(select.disabled).toBe(false);
    expect(select.value).toBe("workspace-write");
    expect([...select.options].map((option) => option.value)).toEqual([
      "danger-full-access",
      "workspace-write",
      "read-only",
    ]);

    fireEvent.change(select, { target: { value: "read-only" } });
    expect(onChange).toHaveBeenCalledWith("read-only");
  });

  it("defaults to full access when the agent predates the field", () => {
    renderWithI18n(
      <SandboxSettingField
        label="Permissions"
        value={undefined}
        provider="dsh"
        canEdit
        onChange={vi.fn()}
      />,
    );

    const select = screen.getByLabelText("Permissions") as HTMLSelectElement;
    expect(select.value).toBe("danger-full-access");
  });

  it("stays visible but disabled where the runtime ignores it, with the reason spelled out", () => {
    render(
      <SandboxSettingField
        label="Permissions"
        value="danger-full-access"
        provider="codex"
        canEdit
        onChange={vi.fn()}
      />,
    );

    const select = screen.getByLabelText("Permissions") as HTMLSelectElement;
    expect(select.disabled).toBe(true);
    // 设置页不该假装一个不生效的控件可用。
    expect(screen.getByText(/pinned to full access/i)).toBeInTheDocument();
  });

  it("cannot be edited by a member who may not change the agent", () => {
    render(
      <SandboxSettingField
        label="Permissions"
        value="danger-full-access"
        provider="dsh"
        canEdit={false}
        onChange={vi.fn()}
      />,
    );

    expect((screen.getByLabelText("Permissions") as HTMLSelectElement).disabled).toBe(true);
  });
});
