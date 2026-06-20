import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement, ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import zhCommon from "../locales/zh-Hans/common.json";
import zhAuth from "../locales/zh-Hans/auth.json";
import zhSettings from "../locales/zh-Hans/settings.json";

const TEST_RESOURCES = {
  "zh-Hans": { common: zhCommon, auth: zhAuth, settings: zhSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function renderWithI18n(ui: ReactElement) {
  return render(ui, { wrapper: I18nWrapper });
}

const mockLogin = vi.hoisted(() => vi.fn());
const mockApiLogin = vi.hoisted(() => vi.fn());
const mockApiListWorkspaces = vi.hoisted(() => vi.fn());
const mockApiSetToken = vi.hoisted(() => vi.fn());
const mockApiGetMe = vi.hoisted(() => vi.fn());
const mockApiIssueCliToken = vi.hoisted(() => vi.fn());
const mockSetQueryData = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>(
    "@tanstack/react-query",
  );
  return { ...actual, useQueryClient: () => ({ setQueryData: mockSetQueryData }) };
});

vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: (s: unknown) => unknown) => {
      const state = { login: mockLogin };
      return selector ? selector(state) : state;
    },
    {
      getState: () => ({
        login: mockLogin,
      }),
    },
  ),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    login: mockApiLogin,
    listWorkspaces: mockApiListWorkspaces,
    setToken: mockApiSetToken,
    getMe: mockApiGetMe,
    issueCliToken: mockApiIssueCliToken,
  },
}));

vi.mock("@multica/core/types", () => ({}));

import { LoginPage, validateCliCallback } from "./login-page";

describe("LoginPage", () => {
  const onSuccess = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    mockApiGetMe.mockRejectedValue(new Error("unauthorized"));
    mockApiListWorkspaces.mockResolvedValue([]);
    localStorage.clear();
    Object.defineProperty(window, "location", {
      writable: true,
      value: { href: "http://localhost:3000" },
    });
  });

  it("renders account and password fields", () => {
    renderWithI18n(<LoginPage onSuccess={onSuccess} />);

    expect(screen.getByText("登录 Multica")).toBeInTheDocument();
    expect(screen.getByLabelText("账号")).toBeInTheDocument();
    expect(screen.getByLabelText("密码")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "继续" })).toBeInTheDocument();
  });

  it("logs in with account and password", async () => {
    mockLogin.mockResolvedValueOnce({ id: "u1", account: "alice", name: "Alice" });
    const user = userEvent.setup();
    renderWithI18n(<LoginPage onSuccess={onSuccess} />);

    await user.type(screen.getByLabelText("账号"), "alice");
    await user.type(screen.getByLabelText("密码"), "correct-password");
    await user.click(screen.getByRole("button", { name: "继续" }));

    await waitFor(() => {
      expect(mockLogin).toHaveBeenCalledWith("alice", "correct-password");
      expect(mockApiListWorkspaces).toHaveBeenCalled();
      expect(onSuccess).toHaveBeenCalled();
    });
  });

  it("shows login errors", async () => {
    mockLogin.mockRejectedValueOnce(new Error("账号或密码错误"));
    const user = userEvent.setup();
    renderWithI18n(<LoginPage onSuccess={onSuccess} />);

    await user.type(screen.getByLabelText("账号"), "alice");
    await user.type(screen.getByLabelText("密码"), "wrong-password");
    await user.click(screen.getByRole("button", { name: "继续" }));

    expect(await screen.findByText("账号或密码错误")).toBeInTheDocument();
  });

  it("uses direct API login for CLI callback", async () => {
    mockApiLogin.mockResolvedValueOnce({ token: "jwt-token", user: { id: "u1" } });
    const user = userEvent.setup();
    renderWithI18n(
      <LoginPage
        onSuccess={onSuccess}
        cliCallback={{ url: "http://localhost:39876/callback", state: "state-1" }}
      />,
    );

    await user.type(screen.getByLabelText("账号"), "alice");
    await user.type(screen.getByLabelText("密码"), "correct-password");
    await user.click(screen.getByRole("button", { name: "继续" }));

    await waitFor(() => {
      expect(mockApiLogin).toHaveBeenCalledWith("alice", "correct-password");
      expect(window.location.href).toBe(
        "http://localhost:39876/callback?token=jwt-token&state=state-1",
      );
    });
  });

  it("validates CLI callback hosts", () => {
    expect(validateCliCallback("http://localhost:39876/callback")).toBe(true);
    expect(validateCliCallback("http://192.168.1.20:39876/callback")).toBe(true);
    expect(validateCliCallback("https://localhost:39876/callback")).toBe(false);
    expect(validateCliCallback("http://example.com/callback")).toBe(false);
  });
});
