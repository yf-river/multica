import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "@multica/views/locales/en/common.json";
import enAuth from "@multica/views/locales/en/auth.json";
import enSettings from "@multica/views/locales/en/settings.json";
import type { ReactNode } from "react";

const TEST_RESOURCES = {
  en: { common: enCommon, auth: enAuth, settings: enSettings },
};

function createWrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) => (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </I18nProvider>
  );
}

const {
  mockLogin,
  mockIssueCliToken,
  mockListWorkspaces,
  mockRouterPush,
  mockRouterReplace,
  searchParamsState,
  authStateRef,
} = vi.hoisted(() => ({
  mockLogin: vi.fn(),
  mockIssueCliToken: vi.fn(),
  mockListWorkspaces: vi.fn(),
  mockRouterPush: vi.fn(),
  mockRouterReplace: vi.fn(),
  searchParamsState: { params: new URLSearchParams() },
  authStateRef: {
    state: {
      login: vi.fn(),
      user: null as null | { id: string; email: string; onboarded_at?: string | null },
      isLoading: false,
    },
  },
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: mockRouterPush, replace: mockRouterReplace }),
  usePathname: () => "/login",
  useSearchParams: () => searchParamsState.params,
}));

vi.mock("@multica/core/auth", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/auth")>(
      "@multica/core/auth",
    );
  authStateRef.state.login = mockLogin;
  const useAuthStore = Object.assign(
    (selector: (s: typeof authStateRef.state) => unknown) =>
      selector(authStateRef.state),
    { getState: () => authStateRef.state },
  );
  return { ...actual, useAuthStore };
});

vi.mock("@/features/auth/auth-cookie", () => ({
  setLoggedInCookie: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listWorkspaces: mockListWorkspaces,
    listMyInvitations: vi.fn().mockResolvedValue([]),
    setToken: vi.fn(),
    getMe: vi.fn().mockRejectedValue(new Error("unauthorized")),
    issueCliToken: mockIssueCliToken,
  },
}));

import LoginPage from "./page";

describe("LoginPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    searchParamsState.params = new URLSearchParams();
    authStateRef.state.user = null;
    authStateRef.state.isLoading = false;
    mockListWorkspaces.mockResolvedValue([]);
  });

  it("renders account/password login form", () => {
    render(<LoginPage />, { wrapper: createWrapper() });

    expect(screen.getByText("Sign in to Multica")).toBeInTheDocument();
    expect(screen.getByLabelText("Account")).toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
  });

  it("logs in through account/password and routes after success", async () => {
    mockLogin.mockResolvedValueOnce({ id: "u1", email: "alice", onboarded_at: null });
    const user = userEvent.setup();
    render(<LoginPage />, { wrapper: createWrapper() });

    await user.type(screen.getByLabelText("Account"), "alice");
    await user.type(screen.getByLabelText("Password"), "correct-password");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => {
      expect(mockLogin).toHaveBeenCalledWith("alice", "correct-password");
      expect(mockRouterPush).toHaveBeenCalled();
    });
  });

  it("mints a token and deep-links to Desktop when already logged in with platform=desktop", async () => {
    searchParamsState.params = new URLSearchParams({ platform: "desktop" });
    authStateRef.state.user = {
      id: "u1",
      email: "alice",
      onboarded_at: "2026-01-01T00:00:00Z",
    };
    mockIssueCliToken.mockResolvedValue({ token: "handoff-jwt" });

    Object.defineProperty(window, "location", {
      writable: true,
      value: { href: "http://localhost:3000/login?platform=desktop" },
    });

    render(<LoginPage />, { wrapper: createWrapper() });

    await waitFor(() => {
      expect(mockIssueCliToken).toHaveBeenCalled();
      expect(window.location.href).toBe("multica://auth/callback?token=handoff-jwt");
    });
  });
});
