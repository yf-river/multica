import { beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";

const coreProviderProps = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/platform", () => ({
  CoreProvider: ({ children, ...props }: { children: React.ReactNode }) => {
    coreProviderProps(props);
    return children;
  },
}));

vi.mock("@/platform/navigation", () => ({
  WebNavigationProvider: ({ children }: { children: React.ReactNode }) => children,
}));

vi.mock("@/features/auth/auth-cookie", () => ({
  setLoggedInCookie: vi.fn(),
  clearLoggedInCookie: vi.fn(),
}));

vi.mock("./pageview-tracker", () => ({
  PageviewTracker: () => null,
}));

import { WebProviders } from "./web-providers";

describe("WebProviders", () => {
  beforeEach(() => {
    coreProviderProps.mockClear();
    localStorage.clear();
  });

  it("always uses HttpOnly cookie authentication", () => {
    localStorage.setItem("multica_token", "stale-browser-token");

    render(
      <WebProviders locale="zh-Hans" resources={{}}>
        <div>content</div>
      </WebProviders>,
    );

    expect(coreProviderProps).toHaveBeenCalledWith(
      expect.objectContaining({ cookieAuth: true }),
    );
  });
});
