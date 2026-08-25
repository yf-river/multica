import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, fireEvent } from "@testing-library/react";
import { renderWithI18n } from "../test/i18n";
import { NoAccessPage } from "./no-access-page";

const navigate = vi.fn();
const logout = vi.fn();
const mockWorkspaces = vi.hoisted(() => [{ slug: "valid-team" }]);

vi.mock("../navigation", () => ({
  useNavigation: () => ({ push: navigate, replace: navigate }),
}));

vi.mock("../auth", () => ({
  useLogout: () => logout,
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: mockWorkspaces }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  workspaceListOptions: () => ({ queryKey: ["workspaces", "list"] }),
}));

function renderPage() {
  return renderWithI18n(<NoAccessPage />);
}

describe("NoAccessPage", () => {
  beforeEach(() => {
    navigate.mockReset();
    logout.mockReset();
  });

  it("renders generic message that doesn't leak existence", () => {
    renderPage();
    expect(
      screen.getByText(/该工作区不存在，或你没有访问权限/),
    ).toBeInTheDocument();
  });

  it("navigates to the first accessible workspace on 'Go to my workspaces'", () => {
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /前往我的工作区/ }));
    expect(navigate).toHaveBeenCalledWith("/valid-team/issues");
  });

  it("clears last_workspace_slug cookie on mount so the proxy stops looping us back", () => {
    document.cookie = "last_workspace_slug=stale; path=/";
    renderPage();
    const value = document.cookie.match(/last_workspace_slug=([^;]*)/)?.[1];
    expect(value ?? "").toBe("");
  });

  it("fully logs out on 'Sign in as a different user' instead of just navigating", () => {
    renderPage();
    fireEvent.click(
      screen.getByRole("button", { name: /使用其他账号登录/ }),
    );
    expect(logout).toHaveBeenCalledTimes(1);
    expect(navigate).not.toHaveBeenCalledWith("/login");
  });
});
