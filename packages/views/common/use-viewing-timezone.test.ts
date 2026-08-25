import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook } from "@testing-library/react";

const userRef = vi.hoisted(
  () => ({ current: null as { timezone?: string | null } | null }),
);

vi.mock("@multica/core/auth", () => {
  type AuthState = { user: typeof userRef.current };
  const useAuthStore = Object.assign(
    (sel: (s: AuthState) => unknown) => sel({ user: userRef.current }),
    { getState: () => ({ user: userRef.current }) },
  );
  return { useAuthStore };
});

vi.mock("./timezone-select", () => ({
  browserTimezone: () => "America/Chicago",
  isValidTimeZone: (value: string) => value !== "Etc/Unknown",
}));

import { useViewingTimezone } from "./use-viewing-timezone";

describe("useViewingTimezone", () => {
  beforeEach(() => {
    userRef.current = null;
  });

  it("returns the stored preference when the user pinned one", () => {
    userRef.current = { timezone: "Asia/Tokyo" };
    const { result } = renderHook(() => useViewingTimezone());
    expect(result.current).toBe("Asia/Tokyo");
  });

  it.each([
    null,
    { timezone: null },
    { timezone: "   " },
    { timezone: "Etc/Unknown" },
    { timezone: "" },
    undefined,
  ])("falls back to the browser timezone for %j", (user) => {
    userRef.current = user as typeof userRef.current;
    const { result } = renderHook(() => useViewingTimezone());
    expect(result.current).toBe("America/Chicago");
  });
});
