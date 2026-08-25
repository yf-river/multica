import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  queryClear: vi.fn(),
  authLogout: vi.fn(),
  push: vi.fn(),
  resetAccountState: vi.fn(),
  clearAccountStorage: vi.fn(),
  toastError: vi.fn(),
  local: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
  session: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() },
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ clear: mocks.queryClear }),
}));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { logout: () => Promise<void> }) => unknown) =>
    selector({ logout: mocks.authLogout }),
}));
vi.mock("sonner", () => ({
  toast: { error: mocks.toastError },
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
vi.mock("../i18n", () => ({
  useT: () => ({
    t: (selector: (value: { errors: { logout_failed: string } }) => string) =>
      selector({ errors: { logout_failed: "退出失败，请重试。" } }),
  }),
}));

import { useLogout } from "./use-logout";

describe("useLogout", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.authLogout.mockResolvedValue(undefined);
  });

  it("ends the server session before clearing persisted account state", async () => {
    const { result } = renderHook(() => useLogout());
    await act(async () => {
      await result.current();
    });

    expect(mocks.resetAccountState).toHaveBeenCalledTimes(1);
    expect(mocks.clearAccountStorage).toHaveBeenCalledWith({
      local: mocks.local,
      session: mocks.session,
    });
    expect(mocks.queryClear).toHaveBeenCalledTimes(1);
    expect(mocks.authLogout).toHaveBeenCalledTimes(1);
    expect(mocks.push).toHaveBeenCalledWith("/login");
    expect(mocks.authLogout.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.resetAccountState.mock.invocationCallOrder[0] ?? 0,
    );
  });

  it("keeps local state and reports the failure when cookie logout fails", async () => {
    mocks.authLogout.mockRejectedValueOnce(new TypeError("network unavailable"));
    const { result } = renderHook(() => useLogout());

    await act(async () => {
      await result.current();
    });

    expect(mocks.toastError).toHaveBeenCalledWith("退出失败，请重试。");
    expect(mocks.resetAccountState).not.toHaveBeenCalled();
    expect(mocks.clearAccountStorage).not.toHaveBeenCalled();
    expect(mocks.queryClear).not.toHaveBeenCalled();
    expect(mocks.push).not.toHaveBeenCalled();
  });
});
