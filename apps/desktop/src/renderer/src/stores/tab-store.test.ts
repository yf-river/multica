import { describe, expect, it, vi, beforeEach } from "vitest";

// createTabRouter transitively pulls in route modules that expect a browser
// router context. For pure store tests we stub it to a minimal disposable.
const createTabRouterMock = vi.hoisted(() =>
  vi.fn(() => ({
    dispose: vi.fn(),
    state: { location: { pathname: "/" } },
    navigate: vi.fn(),
    subscribe: vi.fn(() => () => {}),
  })),
);
vi.mock("../routes", () => ({
  createTabRouter: createTabRouterMock,
}));

import { useTabStore } from "./tab-store";

beforeEach(() => {
  createTabRouterMock.mockClear();
  useTabStore.getState().reset();
});

function openTestTab(path: string) {
  const store = useTabStore.getState();
  if (!store.activeWorkspaceSlug) store.switchWorkspace("acme");
  return store.openTab(path, "test", "Circle");
}

describe("tab path boundary", () => {
  it("rejects workspace-less paths", () => {
    expect(["/", ""].map(openTestTab)).toEqual(["", ""]);
    expect(useTabStore.getState().byWorkspace.acme.tabs).toHaveLength(1);
  });

  it("opens valid workspace-scoped paths", () => {
    for (const path of [
      "/acme/issues",
      "/my-team/projects/abc",
      "/acme/debug/prompts",
      "/acme/evaluation/runs",
    ]) {
      const tabId = openTestTab(path);
      expect(tabId).not.toBe("");
      expect(useTabStore.getState().byWorkspace.acme.tabs.find((tab) => tab.id === tabId)?.path)
        .toBe(path);
    }
  });

  it("rejects reserved root paths", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    expect(["/issues", "/settings", "/workspaces/new"].map(openTestTab)).toEqual([
      "",
      "",
      "",
    ]);
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });

  it("accepts non-reserved workspace-like slugs", () => {
    for (const path of ["/acme-issues/issues", "/project-x/inbox"]) {
      expect(openTestTab(path)).not.toBe("");
    }
  });
});

describe("useTabStore actions", () => {
  it("switchWorkspace creates a new group with a default tab on first entry", () => {
    useTabStore.getState().switchWorkspace("acme");
    const s = useTabStore.getState();
    expect(s.activeWorkspaceSlug).toBe("acme");
    expect(s.byWorkspace.acme.tabs).toHaveLength(1);
    expect(s.byWorkspace.acme.tabs[0].path).toBe("/acme/issues");
  });

  it("switchWorkspace without openPath restores the group's last active tab", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    store.openTab("/acme/projects", "项目", "FolderKanban", "new");
    const acmeProjectsId = useTabStore.getState().byWorkspace.acme.tabs[1].id;
    store.setActiveTab(acmeProjectsId);

    // Enter a different workspace then come back
    store.switchWorkspace("butter");
    expect(useTabStore.getState().activeWorkspaceSlug).toBe("butter");

    store.switchWorkspace("acme");
    const s = useTabStore.getState();
    expect(s.activeWorkspaceSlug).toBe("acme");
    expect(s.byWorkspace.acme.activeTabId).toBe(acmeProjectsId);
  });

  it("switchWorkspace with openPath dedupes into an existing tab with same path", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme"); // creates default /acme/issues
    store.openTab("/acme/projects", "项目", "FolderKanban", "new");

    store.switchWorkspace("acme", "/acme/issues");
    const s = useTabStore.getState();
    expect(s.byWorkspace.acme.tabs).toHaveLength(2); // no duplicate created
    const activeTab = s.byWorkspace.acme.tabs.find(
      (t) => t.id === s.byWorkspace.acme.activeTabId,
    );
    expect(activeTab?.path).toBe("/acme/issues");
  });

  it("switchWorkspace with openPath not matching any tab adds a new tab", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    store.switchWorkspace("acme", "/acme/issues/bug-42");
    const s = useTabStore.getState();
    expect(s.byWorkspace.acme.tabs).toHaveLength(2);
    const activeTab = s.byWorkspace.acme.tabs.find(
      (t) => t.id === s.byWorkspace.acme.activeTabId,
    );
    expect(activeTab?.path).toBe("/acme/issues/bug-42");
  });

  it("openTab dedupes by path within the active workspace", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    const id1 = store.openTab("/acme/projects", "项目", "FolderKanban");
    const id2 = store.openTab("/acme/projects", "项目", "FolderKanban");
    expect(id1).toBe(id2);
    expect(useTabStore.getState().byWorkspace.acme.tabs).toHaveLength(2); // default + projects
  });

  it("openTab new mode creates another tab for the same path", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");

    const id = store.openTab("/acme/issues", "任务", "ListTodo", "new");

    expect(id).not.toBe(useTabStore.getState().byWorkspace.acme.tabs[0].id);
    expect(useTabStore.getState().byWorkspace.acme.tabs).toHaveLength(2);
  });

  it("closeTab on the last tab in a workspace reseeds the default tab", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    const onlyTabId = useTabStore.getState().byWorkspace.acme.tabs[0].id;
    store.closeTab(onlyTabId);
    const s = useTabStore.getState();
    expect(s.byWorkspace.acme.tabs).toHaveLength(1);
    expect(s.byWorkspace.acme.tabs[0].path).toBe("/acme/issues");
    expect(s.byWorkspace.acme.tabs[0].id).not.toBe(onlyTabId); // fresh tab
  });

  it("defers disposing the closed tab router until after the store update", () => {
    vi.useFakeTimers();
    try {
      const store = useTabStore.getState();
      store.switchWorkspace("acme");
      const closedTabId = store.openTab("/acme/settings", "Settings", "Settings", "new");
      const closingTab = useTabStore
        .getState()
        .byWorkspace.acme.tabs.find((t) => t.id === closedTabId);
      const dispose = vi.mocked(closingTab!.router.dispose);

      store.closeTab(closedTabId);

      expect(dispose).not.toHaveBeenCalled();
      expect(
        useTabStore.getState().byWorkspace.acme.tabs.some((t) => t.id === closedTabId),
      ).toBe(false);

      vi.runAllTimers();

      expect(dispose).toHaveBeenCalledOnce();
    } finally {
      vi.useRealTimers();
    }
  });

  it("ignores router-sync updates from a tab after it has been closed", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    const closedTabId = store.openTab("/acme/settings", "Settings", "Settings", "new");

    store.closeTab(closedTabId);
    const before = useTabStore.getState().byWorkspace.acme;

    store.updateTab(closedTabId, { path: "/acme/runtimes", icon: "Monitor" });
    store.updateTabHistory(closedTabId, 1, 2);

    expect(useTabStore.getState().byWorkspace.acme).toBe(before);
    expect(
      useTabStore.getState().byWorkspace.acme.tabs.some((t) => t.id === closedTabId),
    ).toBe(false);
  });

  it("does not replace the tab group for no-op router-sync updates", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    const tab = useTabStore.getState().byWorkspace.acme.tabs[0];
    const before = useTabStore.getState().byWorkspace.acme;

    store.updateTab(tab.id, { path: tab.path, icon: tab.icon, title: tab.title });
    store.updateTabHistory(tab.id, tab.historyIndex, tab.historyLength);

    expect(useTabStore.getState().byWorkspace.acme).toBe(before);
  });

  it("validateWorkspaceSlugs drops groups for slugs not in the valid set and repoints active", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    store.switchWorkspace("butter");
    store.switchWorkspace("acme");
    expect(useTabStore.getState().activeWorkspaceSlug).toBe("acme");

    // Admin removed the user from acme
    store.validateWorkspaceSlugs(new Set(["butter"]));
    const s = useTabStore.getState();
    expect(Object.keys(s.byWorkspace)).toEqual(["butter"]);
    expect(s.activeWorkspaceSlug).toBe("butter");
  });

  it("validateWorkspaceSlugs sets activeWorkspaceSlug to null when all groups are dropped", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    store.validateWorkspaceSlugs(new Set());
    const s = useTabStore.getState();
    expect(s.byWorkspace).toEqual({});
    expect(s.activeWorkspaceSlug).toBeNull();
  });

  it("validateWorkspaceSlugs seeds the first valid workspace when no group exists", () => {
    const store = useTabStore.getState();
    store.validateWorkspaceSlugs(new Set(["acme", "butter"]));
    const s = useTabStore.getState();
    expect(s.activeWorkspaceSlug).toBe("acme");
    expect(s.byWorkspace.acme.tabs).toHaveLength(1);
    expect(s.byWorkspace.acme.tabs[0].path).toBe("/acme/issues");
  });

  it("validateWorkspaceSlugs reactivates an existing valid group before seeding", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    const existingTabId = useTabStore.getState().byWorkspace.acme.tabs[0].id;

    useTabStore.setState({ activeWorkspaceSlug: null });
    store.validateWorkspaceSlugs(new Set(["acme"]));

    const s = useTabStore.getState();
    expect(s.activeWorkspaceSlug).toBe("acme");
    expect(s.byWorkspace.acme.tabs).toHaveLength(1);
    expect(s.byWorkspace.acme.tabs[0].id).toBe(existingTabId);
  });

  it("validateWorkspaceSlugs seeds a fresh tab for a valid slug after dropping all stale groups", () => {
    const store = useTabStore.getState();
    // The only persisted group points at a workspace the user has lost access
    // to — the stale-tab heal path WorkspaceRouteLayout drives.
    store.switchWorkspace("stale");
    const staleRouter = useTabStore.getState().byWorkspace.stale.tabs[0].router;

    store.validateWorkspaceSlugs(new Set(["acme"]));

    const s = useTabStore.getState();
    expect(Object.keys(s.byWorkspace)).toEqual(["acme"]);
    expect(s.activeWorkspaceSlug).toBe("acme");
    expect(s.byWorkspace.acme.tabs).toHaveLength(1);
    expect(s.byWorkspace.acme.tabs[0].path).toBe("/acme/issues");
    // The dropped stale group's router must be disposed, not leaked.
    expect(staleRouter.dispose).toHaveBeenCalled();
  });

  it("reset wipes the whole store", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    store.switchWorkspace("butter");
    store.reset();
    const s = useTabStore.getState();
    expect(s.activeWorkspaceSlug).toBeNull();
    expect(s.byWorkspace).toEqual({});
  });

  it("setActiveTab across workspaces also flips the active workspace", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    store.switchWorkspace("butter");
    const acmeTabId = useTabStore.getState().byWorkspace.acme.tabs[0].id;
    store.setActiveTab(acmeTabId);
    expect(useTabStore.getState().activeWorkspaceSlug).toBe("acme");
  });
});

describe("togglePin", () => {
  it("flips a tab's pinned state", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    const tabId = useTabStore.getState().byWorkspace.acme.tabs[0].id;
    expect(useTabStore.getState().byWorkspace.acme.tabs[0].pinned).toBe(false);

    store.togglePin(tabId);
    expect(useTabStore.getState().byWorkspace.acme.tabs[0].pinned).toBe(true);

    store.togglePin(tabId);
    expect(useTabStore.getState().byWorkspace.acme.tabs[0].pinned).toBe(false);
  });

  it("moves a newly-pinned tab to the start of the pinned zone", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme"); // creates default unpinned tab at index 0
    store.openTab("/acme/projects", "项目", "FolderKanban", "new");
    store.openTab("/acme/agents", "Agents", "Bot", "new");
    const agentsId = useTabStore.getState().byWorkspace.acme.tabs[2].id;

    store.togglePin(agentsId);
    const tabs = useTabStore.getState().byWorkspace.acme.tabs;
    expect(tabs[0].id).toBe(agentsId);
    expect(tabs[0].pinned).toBe(true);
    expect(tabs[1].pinned).toBe(false);
    expect(tabs[2].pinned).toBe(false);
  });

  it("appends a second pinned tab after the first pinned tab", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    store.openTab("/acme/projects", "项目", "FolderKanban", "new");
    store.openTab("/acme/agents", "Agents", "Bot", "new");
    const projectsId = useTabStore.getState().byWorkspace.acme.tabs[1].id;
    const agentsId = useTabStore.getState().byWorkspace.acme.tabs[2].id;

    store.togglePin(agentsId);
    store.togglePin(projectsId);

    // Both pinned, in the order they were pinned (agents first, projects
    // second), then the unpinned default tab.
    const tabs = useTabStore.getState().byWorkspace.acme.tabs;
    expect(tabs.map((t) => t.id)).toEqual([
      agentsId,
      projectsId,
      tabs[2].id,
    ]);
    expect(tabs.map((t) => t.pinned)).toEqual([true, true, false]);
  });

  it("returns an unpinned tab to the start of the unpinned zone", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    store.openTab("/acme/projects", "项目", "FolderKanban", "new");
    const issuesId = useTabStore.getState().byWorkspace.acme.tabs[0].id;
    const projectsId = useTabStore.getState().byWorkspace.acme.tabs[1].id;

    // Pin both, then unpin one.
    store.togglePin(issuesId);
    store.togglePin(projectsId);
    store.togglePin(issuesId);

    const tabs = useTabStore.getState().byWorkspace.acme.tabs;
    expect(tabs.map((t) => t.id)).toEqual([projectsId, issuesId]);
    expect(tabs.map((t) => t.pinned)).toEqual([true, false]);
  });
});

describe("moveTab boundary clamp", () => {
  it("clamps a pinned-tab move so it never crosses into the unpinned zone", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    store.openTab("/acme/projects", "项目", "FolderKanban", "new");
    store.openTab("/acme/agents", "Agents", "Bot", "new");
    const issuesId = useTabStore.getState().byWorkspace.acme.tabs[0].id;

    store.togglePin(issuesId); // [issues(pinned), projects, agents]

    // User tries to drag the pinned tab to index 2 (unpinned zone end).
    store.moveTab(0, 2);
    const tabs = useTabStore.getState().byWorkspace.acme.tabs;
    // It should be clamped to index 0 — the only pinned slot — i.e. unchanged.
    expect(tabs[0].id).toBe(issuesId);
    expect(tabs.map((t) => t.pinned)).toEqual([true, false, false]);
  });

  it("clamps an unpinned-tab move so it never crosses into the pinned zone", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    store.openTab("/acme/projects", "项目", "FolderKanban", "new");
    store.openTab("/acme/agents", "Agents", "Bot", "new");
    const issuesId = useTabStore.getState().byWorkspace.acme.tabs[0].id;
    const agentsId = useTabStore.getState().byWorkspace.acme.tabs[2].id;

    store.togglePin(issuesId); // [issues(pinned), projects, agents]

    // User tries to drag agents (index 2) to index 0 (pinned zone).
    store.moveTab(2, 0);
    const tabs = useTabStore.getState().byWorkspace.acme.tabs;
    // Clamped to index 1 — start of the unpinned zone.
    expect(tabs[0].id).toBe(issuesId);
    expect(tabs[1].id).toBe(agentsId);
    expect(tabs.map((t) => t.pinned)).toEqual([true, false, false]);
  });

  it("reorders freely within the same zone", () => {
    const store = useTabStore.getState();
    store.switchWorkspace("acme");
    store.openTab("/acme/projects", "项目", "FolderKanban", "new");
    store.openTab("/acme/agents", "Agents", "Bot", "new");

    // All unpinned; move agents (2) to position 0.
    store.moveTab(2, 0);
    const tabs = useTabStore.getState().byWorkspace.acme.tabs;
    expect(tabs.map((t) => t.path)).toEqual([
      "/acme/agents",
      "/acme/issues",
      "/acme/projects",
    ]);
  });
});
