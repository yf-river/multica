import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  queryClear: vi.fn(),
  authLogout: vi.fn(),
  push: vi.fn(),
  resetAccountState: vi.fn(),
  clearAccountStorage: vi.fn(),
  local: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
  session: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ clear: mocks.queryClear }),
}));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { logout: () => void }) => unknown) =>
    selector({ logout: mocks.authLogout }),
}));
vi.mock("@multica/core/platform", () => ({
  clearAccountStorage: mocks.clearAccountStorage,
  defaultStorage: mocks.local,
  defaultSessionStorage: mocks.session,
  resetAccountState: mocks.resetAccountState,
}));
vi.mock("@multica/core/paths", () => ({
  paths: { login: () => "/login" },
}));
vi.mock("../navigation", () => ({
  useNavigation: () => ({ push: mocks.push }),
}));

import { useLogout } from "./use-logout";

describe("useLogout", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("clears in-memory and persisted account state before ending the session", () => {
    const { result } = renderHook(() => useLogout());
    act(() => result.current());

    expect(mocks.resetAccountState).toHaveBeenCalledTimes(1);
    expect(mocks.clearAccountStorage).toHaveBeenCalledWith({
      local: mocks.local,
      session: mocks.session,
    });
    expect(mocks.queryClear).toHaveBeenCalledTimes(1);
    expect(mocks.authLogout).toHaveBeenCalledTimes(1);
    expect(mocks.push).toHaveBeenCalledWith("/login");
    expect(mocks.resetAccountState.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.authLogout.mock.invocationCallOrder[0] ?? 0,
    );
  });
});
