import { render, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { router } = vi.hoisted(() => ({
  router: {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    prefetch: vi.fn(),
  },
}));

vi.mock("next/navigation", () => ({
  useRouter: () => router,
  usePathname: () => "/acme/issues",
  useSearchParams: () => new URLSearchParams(),
}));

import { WebNavigationProvider } from "./navigation";

describe("WebNavigationProvider internal links", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("routes editor internal-navigation events through the Next router", async () => {
    render(
      <WebNavigationProvider>
        <div>content</div>
      </WebNavigationProvider>,
    );

    window.dispatchEvent(
      new CustomEvent("multica:navigate", {
        detail: { path: "/acme/issues/issue-1" },
      }),
    );

    await waitFor(() => {
      expect(router.push).toHaveBeenCalledWith("/acme/issues/issue-1");
    });
  });

  it("ignores malformed and protocol-relative navigation events", () => {
    render(
      <WebNavigationProvider>
        <div>content</div>
      </WebNavigationProvider>,
    );

    window.dispatchEvent(
      new CustomEvent("multica:navigate", { detail: { path: "//evil.test" } }),
    );
    window.dispatchEvent(
      new CustomEvent("multica:navigate", { detail: { path: 42 } }),
    );

    expect(router.push).not.toHaveBeenCalled();
  });
});
