import { describe, it, expect, vi, beforeEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../test/i18n";

async function submitCredentials(password: string) {
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("账号"), "alice");
  await user.type(screen.getByLabelText("密码"), password);
  await user.click(screen.getByRole("button", { name: "继续" }));
}

const mockLogin = vi.hoisted(() => vi.fn());
const mockApiLogin = vi.hoisted(() => vi.fn());
const mockApiListWorkspaces = vi.hoisted(() => vi.fn());
const mockApiSetToken = vi.hoisted(() => vi.fn());
const mockApiGetMe = vi.hoisted(() => vi.fn());
const mockApiIssueCliToken = vi.hoisted(() => vi.fn());
const mockSetQueryData = vi.hoisted(() => vi.fn());
const MockApiError = vi.hoisted(
  () =>
    class MockApiError extends Error {
      constructor(
        message: string,
        readonly status: number,
      ) {
        super(message);
      }
    },
);

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
  ApiError: MockApiError,
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
    mockApiGetMe.mockRejectedValue(new MockApiError("unauthorized", 401));
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
    renderWithI18n(<LoginPage onSuccess={onSuccess} />);
    await submitCredentials("correct-password");

    await waitFor(() => {
      expect(mockLogin).toHaveBeenCalledWith("alice", "correct-password");
      expect(mockApiListWorkspaces).toHaveBeenCalled();
      expect(onSuccess).toHaveBeenCalled();
    });
  });

  it("shows login errors", async () => {
    mockLogin.mockRejectedValueOnce(new Error("账号或密码错误"));
    renderWithI18n(<LoginPage onSuccess={onSuccess} />);
    await submitCredentials("wrong-password");

    expect(await screen.findByText("账号或密码错误")).toBeInTheDocument();
  });

  it("uses direct API login for CLI callback", async () => {
    mockApiLogin.mockResolvedValueOnce({ token: "jwt-token", user: { id: "u1" } });
    renderWithI18n(
      <LoginPage
        onSuccess={onSuccess}
        cliCallback={{ url: "http://localhost:39876/callback", state: "state-1" }}
      />,
    );
    await submitCredentials("correct-password");

    await waitFor(() => {
      expect(mockApiLogin).toHaveBeenCalledWith("alice", "correct-password");
      expect(mockApiSetToken).not.toHaveBeenCalledWith("jwt-token");
      expect(localStorage.getItem("multica_token")).toBeNull();
      expect(window.location.href).toBe(
        "http://localhost:39876/callback?token=jwt-token&state=state-1",
      );
    });
  });

  it("issues a fresh CLI token from the current cookie session", async () => {
    mockApiGetMe.mockResolvedValueOnce({ id: "u1", account: "alice", name: "Alice" });
    mockApiIssueCliToken.mockResolvedValueOnce({ token: "cli-token" });
    const user = userEvent.setup();

    renderWithI18n(
      <LoginPage
        onSuccess={onSuccess}
        cliCallback={{ url: "http://localhost:39876/callback", state: "state-2" }}
      />,
    );

    await user.click(await screen.findByRole("button", { name: "授权" }));

    await waitFor(() => {
      expect(mockApiIssueCliToken).toHaveBeenCalledOnce();
      expect(window.location.href).toBe(
        "http://localhost:39876/callback?token=cli-token&state=state-2",
      );
    });
  });

  it("reports a failed CLI session check when the server is unavailable", async () => {
    const warning = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const error = new MockApiError("unavailable", 503);
    mockApiGetMe.mockRejectedValueOnce(error);

    renderWithI18n(
      <LoginPage
        onSuccess={onSuccess}
        cliCallback={{ url: "http://localhost:39876/callback", state: "state-3" }}
      />,
    );

    await waitFor(() => {
      expect(warning).toHaveBeenCalledWith(
        "[auth] failed to check existing CLI session",
        error,
      );
    });
    warning.mockRestore();
  });

  it("validates CLI callback hosts", () => {
    expect(validateCliCallback("http://localhost:39876/callback")).toBe(true);
    expect(validateCliCallback("http://192.168.1.20:39876/callback")).toBe(true);
    expect(validateCliCallback("https://localhost:39876/callback")).toBe(false);
    expect(validateCliCallback("http://example.com/callback")).toBe(false);
  });
});
