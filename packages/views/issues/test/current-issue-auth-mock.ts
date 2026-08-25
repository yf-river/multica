import { vi } from "vitest";

export const mockIssueAuthUser = {
  id: "user-1",
  account: "test",
  name: "Test User",
};

vi.mock("@multica/core/auth", () => {
  const state = { user: mockIssueAuthUser, isAuthenticated: true };
  return {
    useAuthStore: Object.assign(
      (selector?: (value: typeof state) => unknown) =>
        selector ? selector(state) : state,
      { getState: () => state },
    ),
  };
});
