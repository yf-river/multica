import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "../api/client";
import type { StorageAdapter, User } from "../types";
import { createAuthStore } from "./store";

const fakeUser: User = {
  id: "u1",
  name: "Alice",
  account: "alice",
  avatar_url: null,
} as User;

function makeStorage(initial: Record<string, string> = {}): StorageAdapter & {
  snapshot: () => Record<string, string>;
} {
  const data = { ...initial };
  return {
    getItem: (k) => data[k] ?? null,
    setItem: (k, v) => {
      data[k] = v;
    },
    removeItem: (k) => {
      delete data[k];
    },
    snapshot: () => ({ ...data }),
  };
}

describe("authStore.logout", () => {
  it("keeps the cookie session and client state when server logout fails", async () => {
    const storage = makeStorage({ multica_token: "unrelated-local-value" });
    const logout = vi.fn().mockRejectedValue(new TypeError("network unavailable"));
    const onLogout = vi.fn();
    const api = {
      setToken: vi.fn(),
      logout,
    } as unknown as ApiClient;
    const store = createAuthStore({ api, storage, cookieAuth: true, onLogout });
    store.setState({ user: fakeUser });

    await expect(store.getState().logout()).rejects.toThrow("network unavailable");

    expect(store.getState().user).toEqual(fakeUser);
    expect(storage.snapshot().multica_token).toBe("unrelated-local-value");
    expect(api.setToken).not.toHaveBeenCalled();
    expect(onLogout).not.toHaveBeenCalled();
  });

  it("clears client state only after cookie logout and platform cleanup succeed", async () => {
    const calls: string[] = [];
    const storage = makeStorage({ multica_token: "stale" });
    const api = {
      setToken: vi.fn(() => calls.push("clear-token")),
      logout: vi.fn(async () => {
        calls.push("server-logout");
      }),
    } as unknown as ApiClient;
    const onLogout = vi.fn(async () => {
      calls.push("platform-cleanup");
    });
    const store = createAuthStore({ api, storage, cookieAuth: true, onLogout });
    store.setState({ user: fakeUser });

    await store.getState().logout();

    expect(calls).toEqual(["server-logout", "clear-token", "platform-cleanup"]);
    expect(store.getState().user).toBeNull();
    expect(storage.snapshot().multica_token).toBeUndefined();
  });
});
