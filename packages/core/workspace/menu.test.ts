import { describe, expect, it } from "vitest";
import { paths } from "../paths";
import {
  CONFIGURABLE_MENU_KEYS,
  WORKSPACE_MENU_GROUPS,
  isWorkspaceMenuItemActive,
  isWorkspaceMenuVisible,
  nextHiddenMenuKeys,
  workspaceMenuItemHref,
  type WorkspaceMenuEntry,
} from "./menu";

const ws = paths.workspace("acme");
const ENTRIES = WORKSPACE_MENU_GROUPS.flatMap((group) => group.entries);
const entry = (key: string): WorkspaceMenuEntry => {
  const found = ENTRIES.find((item) => item.key === key);
  if (!found) throw new Error(`missing menu entry: ${key}`);
  return found;
};
const group = (id: string) => {
  const found = WORKSPACE_MENU_GROUPS.find((item) => item.id === id);
  if (!found) throw new Error(`missing menu group: ${id}`);
  return found;
};

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
  it("reads the legacy aggregate life switch as hiding every life tab", () => {
    const settings = { hidden_menu_items: ["life"] };
    for (const tab of ["memory", "experiment", "observers", "chronicle"]) {
      expect(isWorkspaceMenuVisible(settings, tab)).toBe(false);
    }
    expect(isWorkspaceMenuVisible(settings, "health")).toBe(true);
  });
  it("hides one life tab without touching its siblings", () => {
    const settings = { hidden_menu_items: ["experiment"] };
    expect(isWorkspaceMenuVisible(settings, "experiment")).toBe(false);
    expect(isWorkspaceMenuVisible(settings, "memory")).toBe(true);
    expect(isWorkspaceMenuVisible(settings, "observers")).toBe(true);
    expect(isWorkspaceMenuVisible(settings, "chronicle")).toBe(true);
  });
});

describe("workspace menu model", () => {
  it("exposes one unique preference key per sidebar row", () => {
    const keys = ENTRIES.map((item) => item.key);
    expect(new Set(keys).size).toBe(keys.length);
  });
  it("makes every non-fixed row configurable and keeps chat/settings locked", () => {
    expect(CONFIGURABLE_MENU_KEYS).toEqual(
      ENTRIES.filter((item) => item.fixed !== true).map((item) => item.key),
    );
    expect(CONFIGURABLE_MENU_KEYS).toContain("knowledge");
    expect(CONFIGURABLE_MENU_KEYS).toContain("memory");
    expect(CONFIGURABLE_MENU_KEYS).not.toContain("chat");
    expect(CONFIGURABLE_MENU_KEYS).not.toContain("settings");
    expect(CONFIGURABLE_MENU_KEYS).not.toContain("life");
  });
  it("labels sidebar sections and keeps the inbox row unlabeled", () => {
    expect(group("personal").label).toBeUndefined();
    expect(group("workspace").label).toEqual({ section: "sidebar", key: "workspace_group" });
    expect(group("configure").label).toEqual({ section: "sidebar", key: "configure_group" });
    expect(group("living").label).toEqual({ section: "nav", key: "living" });
    expect(group("life").label).toEqual({ section: "nav", key: "life" });
  });
  it("renders the four life tabs as separate rows", () => {
    expect(group("life").entries.map((item) => item.key)).toEqual([
      "memory",
      "experiment",
      "observers",
      "chronicle",
    ]);
  });
});

describe("workspace menu toggles", () => {
  it("writes one row on toggle and keeps the rest of the preference", () => {
    expect(nextHiddenMenuKeys({ hidden_menu_items: ["issues"] }, "projects", false)).toEqual([
      "issues",
      "projects",
    ]);
    expect(nextHiddenMenuKeys({ hidden_menu_items: ["issues"] }, "issues", true)).toEqual([]);
  });
  it("normalizes a legacy aggregate life switch into the four tab keys", () => {
    expect(nextHiddenMenuKeys({ hidden_menu_items: ["life"] }, "memory", true)).toEqual([
      "experiment",
      "observers",
      "chronicle",
    ]);
  });
  it("never writes fixed rows", () => {
    expect(nextHiddenMenuKeys({}, "chat", false)).toEqual([]);
    expect(nextHiddenMenuKeys({}, "settings", false)).toEqual([]);
  });
});

describe("workspace menu destinations", () => {
  it("builds the row href from the workspace paths", () => {
    expect(workspaceMenuItemHref(entry("issues"), ws)).toBe("/acme/issues");
    expect(workspaceMenuItemHref(entry("knowledge"), ws)).toBe("/acme/knowledge");
    expect(workspaceMenuItemHref(entry("experiment"), ws)).toBe("/acme/life?tab=experiment");
  });
  it("matches detail routes for regular pages", () => {
    const active = (key: string, pathname: string, search = "") =>
      isWorkspaceMenuItemActive(entry(key), {
        pathname,
        searchParams: new URLSearchParams(search),
        paths: ws,
      });
    expect(active("issues", "/acme/issues")).toBe(true);
    expect(active("issues", "/acme/issues/MUL-1")).toBe(true);
    expect(active("inbox", "/acme/issues")).toBe(false);
    expect(active("health", "/acme/health")).toBe(true);
  });
  it("keeps the meals row active on the cooking pages", () => {
    const active = (pathname: string) =>
      isWorkspaceMenuItemActive(entry("meals"), {
        pathname,
        searchParams: new URLSearchParams(),
        paths: ws,
      });
    expect(active("/acme/meals")).toBe(true);
    expect(active("/acme/cooking")).toBe(true);
    expect(active("/acme/cooking/recipes/r-1")).toBe(true);
    expect(active("/acme/goals")).toBe(false);
  });
  it("highlights only the life tab named in the URL, defaulting to memory", () => {
    const active = (key: string, search: string) =>
      isWorkspaceMenuItemActive(entry(key), {
        pathname: "/acme/life",
        searchParams: new URLSearchParams(search),
        paths: ws,
      });
    expect(active("memory", "")).toBe(true);
    expect(active("experiment", "")).toBe(false);
    expect(active("experiment", "tab=experiment")).toBe(true);
    expect(active("memory", "tab=experiment")).toBe(false);
    expect(active("memory", "tab=not-a-tab")).toBe(true);
    expect(active("observers", "tab=observers&focus=x")).toBe(true);
    expect(active("memory", "tab=memory")).toBe(true);
    expect(
      isWorkspaceMenuItemActive(entry("memory"), {
        pathname: "/acme/health",
        searchParams: new URLSearchParams("tab=memory"),
        paths: ws,
      }),
    ).toBe(false);
  });
});
