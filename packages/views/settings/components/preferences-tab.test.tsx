import type { ReactNode } from "react";
import {
  describe,
  it,
  expect,
  beforeEach,
  afterEach,
  vi,
} from "vitest";
import { render, screen, cleanup, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import zhCommon from "../../locales/zh-Hans/common.json";
import zhAuth from "../../locales/zh-Hans/auth.json";
import zhSettings from "../../locales/zh-Hans/settings.json";

const mockUpdateMe = vi.hoisted(() => vi.fn());
const mockToastError = vi.hoisted(() => vi.fn());
const mockToastSuccess = vi.hoisted(() => vi.fn());
const mockSetTheme = vi.hoisted(() => vi.fn());
const mockSetUser = vi.hoisted(() => vi.fn());
const userRef = vi.hoisted(() => ({
  current: null as { id: string; timezone?: string | null } | null,
}));

vi.mock("@multica/ui/components/common/theme-provider", () => ({
  useTheme: () => ({ theme: "light", setTheme: mockSetTheme }),
}));

vi.mock("@multica/core/api", () => ({
  api: { updateMe: mockUpdateMe },
}));

vi.mock("sonner", () => ({
  toast: {
    error: mockToastError,
    success: mockToastSuccess,
  },
}));

vi.mock("@multica/core/auth", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/auth")>(
      "@multica/core/auth",
    );
  type AuthState = {
    user: typeof userRef.current;
    setUser: typeof mockSetUser;
  };
  const state = (): AuthState => ({
    user: userRef.current,
    setUser: mockSetUser,
  });
  const useAuthStore = Object.assign(
    (sel?: (s: AuthState) => unknown) =>
      sel ? sel(state()) : state(),
    { getState: state },
  );
  return { ...actual, useAuthStore };
});

import { PreferencesTab } from "./preferences-tab";
import { useCommentComposerStore } from "@multica/core/issues/stores";

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

describe("PreferencesTab — Timezone section", () => {
  // Shrink the picker to the curated COMMON_TIMEZONES fallback. With the
  // real Intl.supportedValuesOf the popup renders ~600 options, and
  // userEvent traversal of that list blew past the per-test timeout on
  // slow CI runners (MUL-4427). Everything these tests pick — Asia/Tokyo
  // and the "(browser)" sentinel — exists in the fallback list too.
  beforeEach(() => {
    vi.clearAllMocks();
    userRef.current = null;
  });

  // Base UI Select portals its popup onto document.body; unmount each
  // render fully between tests so a prior test's trigger/popup can't
  // shadow the next one's.
  afterEach(() => {
    cleanup();
  });

  // Opens the Select popup and clicks the option whose accessible name
  // matches. Re-queries the trigger each call so it operates on the
  // current render, never a stale node.
  async function pickTimezone(
    user: ReturnType<typeof userEvent.setup>,
    name: RegExp | string,
  ) {
    await user.click(screen.getByRole("combobox", { name: "查看时区" }));
    await user.click(await screen.findByRole("option", { name }));
  }

  it("renders the stored timezone in the trigger", () => {
    userRef.current = { id: "user-1", timezone: "Asia/Shanghai" };
    render(<PreferencesTab />, { wrapper: I18nWrapper });

    expect(
      screen.getByRole("combobox", { name: "查看时区" }).textContent,
    ).toContain("Asia/Shanghai");
  });

  // handleChange PATCHes then updates the store asynchronously, so the
  // post-pick assertions must waitFor it to settle.
  it("saving a new timezone PATCHes /api/me and updates the auth store", async () => {
    userRef.current = { id: "user-1", timezone: "Asia/Shanghai" };
    const updatedUser = { id: "user-1", timezone: "Asia/Tokyo" };
    mockUpdateMe.mockResolvedValueOnce(updatedUser);
    const user = userEvent.setup();
    render(<PreferencesTab />, { wrapper: I18nWrapper });

    await pickTimezone(user, "Asia/Tokyo");

    await waitFor(() => {
      expect(mockUpdateMe).toHaveBeenCalledWith({ timezone: "Asia/Tokyo" });
      expect(mockSetUser).toHaveBeenCalledWith(updatedUser);
      expect(mockToastSuccess).toHaveBeenCalledTimes(1);
    });
  });

  it("surfaces a toast when the PATCH fails", async () => {
    userRef.current = { id: "user-1", timezone: "Asia/Shanghai" };
    mockUpdateMe.mockRejectedValueOnce(new Error("network down"));
    const user = userEvent.setup();
    render(<PreferencesTab />, { wrapper: I18nWrapper });

    await pickTimezone(user, "Asia/Tokyo");

    await waitFor(() => {
      expect(mockUpdateMe).toHaveBeenCalledWith({ timezone: "Asia/Tokyo" });
      expect(mockToastError).toHaveBeenCalledTimes(1);
    });
    expect(mockSetUser).not.toHaveBeenCalled();
  });

  it("clearing the preference sends an empty-string timezone", async () => {
    userRef.current = { id: "user-1", timezone: "Asia/Shanghai" };
    const clearedUser = { id: "user-1", timezone: null };
    mockUpdateMe.mockResolvedValueOnce(clearedUser);
    const user = userEvent.setup();
    render(<PreferencesTab />, { wrapper: I18nWrapper });

    // The "(browser)" sentinel option resets the preference to NULL; the
    // wire payload is an empty string the backend translates to NULL.
    await pickTimezone(user, /浏览器/);

    await waitFor(() => {
      expect(mockUpdateMe).toHaveBeenCalledWith({ timezone: "" });
      // The PATCH response (timezone: null) is pushed into the auth store
      // so the picker switches back to "(browser)" without a refetch.
      expect(mockSetUser).toHaveBeenCalledWith(clearedUser);
    });
  });
});

describe("PreferencesTab — Sticky comment bar", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    userRef.current = null;
    useCommentComposerStore.setState({ sticky: true });
  });

  afterEach(() => {
    cleanup();
  });

  it("renders on by default and toggles the preference off with a saved toast", async () => {
    const user = userEvent.setup();
    render(<PreferencesTab />, { wrapper: I18nWrapper });

    const toggle = screen.getByRole("switch", { name: "固定评论栏" });
    expect(toggle).toHaveAttribute("aria-checked", "true");

    await user.click(toggle);

    expect(useCommentComposerStore.getState().sticky).toBe(false);
    expect(toggle).toHaveAttribute("aria-checked", "false");
    expect(mockToastSuccess).toHaveBeenCalledTimes(1);
  });
});
