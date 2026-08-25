import { describe, expect, it, vi, beforeEach } from "vitest";
import { render } from "@testing-library/react";

// vi.hoisted shared state — every store mock reads the same object so each
// test can mutate it then re-render to drive the tracker.
const state = vi.hoisted(() => ({
  user: null as { id: string } | null,
  isOpen: false,
  activeWorkspaceSlug: null as string | null,
  byWorkspace: {} as Record<
    string,
    { activeTabId: string; tabs: { id: string; path: string }[] }
  >,
  capturePageview: vi.fn<(path?: string) => void>(),
}));

vi.mock("@multica/core/analytics", () => ({
  capturePageview: state.capturePageview,
}));

// Auth store — single selector pattern (`s => s.user`).
vi.mock("@multica/core/auth", () => {
  const useAuthStore = (selector: (s: typeof state) => unknown) =>
    selector(state);
  return { useAuthStore };
});

// Window overlay store — same shape.
vi.mock("@/stores/workspace-creation-overlay-store", () => {
  const useWorkspaceCreationOverlayStore = (
    selector: (s: typeof state) => unknown,
  ) => selector(state);
  return { useWorkspaceCreationOverlayStore };
});

// Tab store — selectors read activeWorkspaceSlug + byWorkspace. Also expose
// getState() for the seed pass and the helpers the tracker imports
// (useActiveTabIdentity, getActiveTab) so we don't have to re-import them
// from the real store inside a mocked module.
vi.mock("@/stores/tab-store", () => {
  const useTabStore = Object.assign(
    (selector: (s: typeof state) => unknown) => selector(state),
    { getState: () => state },
  );
  const getActiveTab = (s: typeof state) => {
    const slug = s.activeWorkspaceSlug;
    if (!slug) return null;
    const group = s.byWorkspace[slug];
    if (!group) return null;
    return group.tabs.find((t) => t.id === group.activeTabId) ?? null;
  };
  const useActiveTabIdentity = () => ({
    slug: state.activeWorkspaceSlug,
    tabId: state.activeWorkspaceSlug
      ? (state.byWorkspace[state.activeWorkspaceSlug]?.activeTabId ?? null)
      : null,
  });
  return { useTabStore, getActiveTab, useActiveTabIdentity };
});

import { PageviewTracker } from "./pageview-tracker";

function reset() {
  state.user = { id: "u1" };
  state.isOpen = false;
  state.activeWorkspaceSlug = null;
  state.byWorkspace = {};
  state.capturePageview.mockClear();
}

beforeEach(() => {
  reset();
});

function setWorkspace(
  activeTabId: string,
  tabs: { id: string; path: string }[],
  slug = "acme",
) {
  state.byWorkspace = { [slug]: { activeTabId, tabs } };
  state.activeWorkspaceSlug = slug;
}

function renderWorkspace(
  activeTabId = "tA",
  tabs = [{ id: "tA", path: "/acme/issues" }],
) {
  setWorkspace(activeTabId, tabs);
  return render(<PageviewTracker />);
}

describe("PageviewTracker", () => {
  it("suppresses pageview when switching to a previously-visible tab on its existing path", () => {
    const tabs = [
      { id: "tA", path: "/acme/issues" },
      { id: "tB", path: "/acme/inbox" },
    ];
    const { rerender } = renderWorkspace("tA", tabs);
    // Initial mount on tA — seeded as observed, no pageview because both
    // tabs were already in the persisted store before the tracker mounted.
    expect(state.capturePageview).not.toHaveBeenCalled();

    // Switch to tB (already-known tab on its already-known path).
    setWorkspace("tB", tabs);
    rerender(<PageviewTracker />);
    expect(state.capturePageview).not.toHaveBeenCalled();

    // Switch back to tA — still no pageview.
    setWorkspace("tA", tabs);
    rerender(<PageviewTracker />);
    expect(state.capturePageview).not.toHaveBeenCalled();
  });

  it("fires pageview when a foreground tab is added", () => {
    const { rerender } = renderWorkspace();
    state.capturePageview.mockClear();

    // Simulate a foreground new-tab action (e.g. an explicit "Open in new
    // tab" toolbar button that passes `{ activate: true }`) — tC is
    // appended AND becomes active. `openInNewTab` defaults to background
    // (no `setActiveTab`); only the `activate: true` branch produces the
    // state change this test exercises.
    state.byWorkspace = {
      acme: {
        activeTabId: "tC",
        tabs: [
          { id: "tA", path: "/acme/issues" },
          { id: "tC", path: "/acme/agents" },
        ],
      },
    };
    rerender(<PageviewTracker />);

    expect(state.capturePageview).toHaveBeenCalledTimes(1);
    expect(state.capturePageview).toHaveBeenCalledWith("/acme/agents");
  });

  it("fires pageview when switchWorkspace opens a new path in another workspace", () => {
    const { rerender } = renderWorkspace();
    state.capturePageview.mockClear();

    // Cross-workspace navigation: switchWorkspace("butter", "/butter/inbox")
    // creates a fresh tab in the destination workspace and makes it active.
    state.byWorkspace = {
      acme: { activeTabId: "tA", tabs: [{ id: "tA", path: "/acme/issues" }] },
      butter: {
        activeTabId: "tD",
        tabs: [{ id: "tD", path: "/butter/inbox" }],
      },
    };
    state.activeWorkspaceSlug = "butter";
    rerender(<PageviewTracker />);

    expect(state.capturePageview).toHaveBeenCalledTimes(1);
    expect(state.capturePageview).toHaveBeenCalledWith("/butter/inbox");
  });

  it("fires pageview on intra-tab navigation (path changes for the same tabId)", () => {
    const { rerender } = renderWorkspace();
    state.capturePageview.mockClear();

    setWorkspace("tA", [{ id: "tA", path: "/acme/issues/123" }]);
    rerender(<PageviewTracker />);

    expect(state.capturePageview).toHaveBeenCalledTimes(1);
    expect(state.capturePageview).toHaveBeenCalledWith("/acme/issues/123");
  });

  it("fires overlay and login pageviews and suppresses re-entry into the same tab afterward", () => {
    const { rerender } = renderWorkspace();
    state.capturePageview.mockClear();

    // Open workspace-creation overlay.
    state.isOpen = true;
    rerender(<PageviewTracker />);
    expect(state.capturePageview).toHaveBeenLastCalledWith("/workspaces/new");

    // Close overlay back to the tab — the tab is already observed on
    // /acme/issues so this is a re-activation, no pageview.
    state.capturePageview.mockClear();
    state.isOpen = false;
    rerender(<PageviewTracker />);
    expect(state.capturePageview).not.toHaveBeenCalled();

    // Logout fires /login.
    state.user = null;
    rerender(<PageviewTracker />);
    expect(state.capturePageview).toHaveBeenLastCalledWith("/login");
  });

  it("suppresses on initial mount when the active tab was restored from persistence", () => {
    renderWorkspace();
    // Restored tab — seeded, treated as a re-activation.
    expect(state.capturePageview).not.toHaveBeenCalled();
  });
});
