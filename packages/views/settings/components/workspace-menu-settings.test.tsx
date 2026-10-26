import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales-test/en/common.json";
import enLayout from "../../locales-test/en/layout.json";
import enSettings from "../../locales-test/en/settings.json";

const update = vi.hoisted(() => ({
  mutateAsync: vi.fn(async () => ({})),
  isPending: false,
  isError: false,
  isSuccess: false,
  error: null as Error | null,
}));

vi.mock("@multica/core/workspace", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace")>()),
  useUpdateWorkspaceMenu: () => update,
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { WorkspaceMenuSettings } from "./workspace-menu-settings";
import type { Workspace } from "@multica/core/types";

const TEST_RESOURCES = {
  en: { common: enCommon, layout: enLayout, settings: enSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

const baseWorkspace: Workspace = {
  id: "workspace-1",
  name: "Acme",
  slug: "acme",
  description: null,
  context: null,
  settings: {},
  repos: [],
  issue_prefix: "ACM",
  avatar_url: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function renderMenu(settings: Record<string, unknown> = {}, canManage = true) {
  return render(
    <WorkspaceMenuSettings
      workspace={{ ...baseWorkspace, settings }}
      canManage={canManage}
    />,
    { wrapper: I18nWrapper },
  );
}

describe("WorkspaceMenuSettings", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    update.mutateAsync.mockResolvedValue({});
    update.isPending = false;
    update.isError = false;
    update.isSuccess = false;
    update.error = null;
  });

  it("mirrors the sidebar sections and renders one switch per row", () => {
    renderMenu();
    for (const heading of ["Workspace", "Configure", "Living", "Life"]) {
      expect(screen.getByRole("heading", { name: heading })).toBeInTheDocument();
    }
    // 21 hideable rows plus the two locked rows.
    expect(screen.getAllByRole("switch")).toHaveLength(23);
    for (const name of ["Memory", "Experiments", "Observation seat", "Chronicle", "Knowledge"]) {
      expect(screen.getByRole("switch", { name })).toBeInTheDocument();
    }
  });

  it("keeps chat and settings always shown and locked", () => {
    renderMenu();
    for (const name of ["Chat", "Settings"]) {
      const toggle = screen.getByRole("switch", { name });
      expect(toggle).toBeChecked();
      expect(toggle).toHaveAttribute("aria-disabled", "true");
      expect(screen.getAllByText("Always shown")).toHaveLength(2);
    }
  });

  it("writes one preference key when a row is toggled", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("switch", { name: "Memory" }));
    expect(update.mutateAsync).toHaveBeenCalledWith({
      workspace: expect.objectContaining({ id: "workspace-1" }),
      hidden: ["memory"],
    });
  });

  it("expands a legacy aggregate life preference on the next toggle", async () => {
    const user = userEvent.setup();
    renderMenu({ hidden_menu_items: ["life"] });
    const memory = screen.getByRole("switch", { name: "Memory" });
    expect(memory).not.toBeChecked();
    await user.click(memory);
    expect(update.mutateAsync).toHaveBeenCalledWith({
      workspace: expect.objectContaining({ id: "workspace-1" }),
      hidden: ["experiment", "observers", "chronicle"],
    });
  });

  it("still lists a fully hidden section so it can be restored", () => {
    renderMenu({ hidden_menu_items: ["health", "meals", "goals"] });
    expect(screen.getByRole("heading", { name: "Living" })).toBeInTheDocument();
    for (const name of ["Body Data", "Meals", "Goals"]) {
      expect(screen.getByRole("switch", { name })).not.toBeChecked();
    }
  });

  it("disables every switch for members who cannot manage the workspace", () => {
    renderMenu({}, false);
    for (const toggle of screen.getAllByRole("switch")) {
      expect(toggle).toHaveAttribute("aria-disabled", "true");
    }
  });
});
