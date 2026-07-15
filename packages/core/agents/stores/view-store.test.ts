// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { useAgentsViewStore } from "./view-store";
import { setCurrentWorkspace } from "../../platform/workspace-storage";

const flush = () => new Promise((resolve) => queueMicrotask(() => resolve(null)));

function persistScope(slug: string, scope: "all" | "mine") {
  localStorage.setItem(
    `multica_agents_view:${slug}`,
    JSON.stringify({ state: { scope }, version: 1 }),
  );
}

async function switchWorkspace(slug: string, id: string) {
  setCurrentWorkspace(slug, id);
  await flush();
  await flush();
}

// Node 25 ships a partial `localStorage` shim under jsdom that's missing
// `clear`/`removeItem`; replace it with a real in-memory Storage so persist
// can round-trip values.
beforeAll(() => {
  if (typeof globalThis.localStorage?.clear !== "function") {
    const values = new Map<string, string>();
    const storage: Storage = {
      get length() { return values.size; },
      clear: () => values.clear(),
      getItem: (k) => values.get(k) ?? null,
      key: (i) => Array.from(values.keys())[i] ?? null,
      removeItem: (k) => { values.delete(k); },
      setItem: (k, v) => { values.set(k, v); },
    };
    Object.defineProperty(globalThis, "localStorage", { configurable: true, value: storage });
    Object.defineProperty(window, "localStorage", { configurable: true, value: storage });
  }
});

beforeEach(() => {
  localStorage.clear();
  useAgentsViewStore.setState({
    scope: "mine",
    sortField: "lastActive",
    sortDirection: "desc",
    hiddenColumns: ["model", "created"],
    filters: { availability: [], runtimes: [], owners: [], models: [] },
  });
  setCurrentWorkspace(null, null);
});

afterEach(() => {
  setCurrentWorkspace(null, null);
});

describe("useAgentsViewStore", () => {
  it("defaults to 'mine'", () => {
    expect(useAgentsViewStore.getState().scope).toBe("mine");
  });

  it("setScope mutates the store", () => {
    useAgentsViewStore.getState().setScope("all");
    expect(useAgentsViewStore.getState().scope).toBe("all");
  });

  it("applies the shared sort, column, and filter transitions", () => {
    const store = useAgentsViewStore.getState();

    store.toggleSort("lastActive");
    expect(useAgentsViewStore.getState().sortDirection).toBe("asc");

    store.setSortField("name");
    expect(useAgentsViewStore.getState()).toMatchObject({
      sortField: "name",
      sortDirection: "asc",
    });

    store.toggleColumn("model");
    expect(useAgentsViewStore.getState().hiddenColumns).not.toContain("model");

    store.toggleFilter("owners", "user-1");
    expect(useAgentsViewStore.getState()).toMatchObject({
      scope: "all",
      filters: { owners: ["user-1"] },
    });

    useAgentsViewStore.getState().clearFilters();
    expect(useAgentsViewStore.getState().filters).toEqual({
      availability: [],
      runtimes: [],
      owners: [],
      models: [],
    });
  });

  it("partialize persists only view prefs (no actions) under the workspace-namespaced key", async () => {
    setCurrentWorkspace("acme", "ws_a");
    await flush();
    useAgentsViewStore.getState().setScope("all");

    const raw = localStorage.getItem("multica_agents_view:acme");
    expect(raw).not.toBeNull();
    const parsed = JSON.parse(raw as string);
    expect(Object.keys(parsed.state).sort()).toEqual([
      "filters",
      "hiddenColumns",
      "scope",
      "sortDirection",
      "sortField",
    ]);
    expect(parsed.state.scope).toBe("all");
  });

  it("rehydrates a different saved scope on workspace switch", async () => {
    persistScope("acme", "all");
    persistScope("beta", "mine");

    await switchWorkspace("acme", "ws_a");
    expect(useAgentsViewStore.getState().scope).toBe("all");

    await switchWorkspace("beta", "ws_b");
    expect(useAgentsViewStore.getState().scope).toBe("mine");
  });

  it("resets to 'mine' when switching to a workspace with no persisted value", async () => {
    persistScope("acme", "all");

    await switchWorkspace("acme", "ws_a");
    expect(useAgentsViewStore.getState().scope).toBe("all");

    await switchWorkspace("beta", "ws_b");
    expect(useAgentsViewStore.getState().scope).toBe("mine");
    expect(localStorage.getItem("multica_agents_view:acme")).not.toBeNull();
  });

});
