import { describe, it, expect, vi, beforeEach } from "vitest";
import { render } from "@testing-library/react";

const { state, capturePageview } = vi.hoisted(() => ({
  state: { pathname: "/" as string | null },
  capturePageview: vi.fn<(path?: string) => void>(),
}));

vi.mock("next/navigation", () => ({
  usePathname: () => state.pathname,
}));

vi.mock("@multica/core/analytics", () => ({
  capturePageview,
}));

import { PageviewTracker } from "./pageview-tracker";

beforeEach(() => {
  state.pathname = "/";
  capturePageview.mockClear();
});

describe("web PageviewTracker", () => {
  it("captures the pathname on mount and on each pathname change", () => {
    const { rerender } = render(<PageviewTracker />);
    expect(capturePageview).toHaveBeenCalledTimes(1);
    expect(capturePageview).toHaveBeenLastCalledWith("/");

    state.pathname = "/acme/issues";
    rerender(<PageviewTracker />);
    expect(capturePageview).toHaveBeenCalledTimes(2);
    expect(capturePageview).toHaveBeenLastCalledWith("/acme/issues");
  });

  it("does not re-capture on a query-string-only navigation", () => {
    state.pathname = "/acme/issues";
    const { rerender } = render(<PageviewTracker />);
    expect(capturePageview).toHaveBeenCalledTimes(1);

    rerender(<PageviewTracker />);
    expect(capturePageview).toHaveBeenCalledTimes(1);
  });
});
