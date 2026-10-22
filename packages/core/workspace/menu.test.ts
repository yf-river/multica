import { describe, expect, it } from "vitest";
import { isWorkspaceMenuVisible } from "./menu";

describe("workspace navigation", () => {
  it("keeps chat and recovery settings accessible even with malformed or old preferences", () => {
    for (const settings of [undefined, {}, { hidden_menu_items: true }, { hidden_menu_items: ["chat", "settings", "issues"] }]) {
      expect(isWorkspaceMenuVisible(settings, "chat")).toBe(true);
      expect(isWorkspaceMenuVisible(settings, "settings")).toBe(true);
      expect(isWorkspaceMenuVisible(settings, "myIssues")).toBe(false);
    }
  });
  it("defaults existing workspaces to visible and isolates their preferences", () => {
    expect(isWorkspaceMenuVisible({}, "issues")).toBe(true);
    expect(isWorkspaceMenuVisible({ hidden_menu_items: ["issues"] }, "issues")).toBe(false);
    expect(isWorkspaceMenuVisible({ hidden_menu_items: ["issues"] }, "agents")).toBe(true);
    expect(isWorkspaceMenuVisible({ hidden_menu_items: [] }, "issues")).toBe(true);
  });
});
