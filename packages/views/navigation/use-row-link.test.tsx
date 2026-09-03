import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { NavigationProvider } from "./context";
import { rowLinkInteractiveProps, useRowLink } from "./use-row-link";
import type { NavigationAdapter } from "./types";

function makeAdapter(
  overrides: Partial<NavigationAdapter> = {},
): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
    ...overrides,
  };
}

function Probe({ href = "/acme/projects/p1" }: { href?: string }) {
  const rowLink = useRowLink();
  return (
    <div role="row" {...rowLink(href)}>
      row
    </div>
  );
}

function renderProbe(adapter: NavigationAdapter) {
  return render(
    <NavigationProvider value={adapter}>
      <Probe />
    </NavigationProvider>,
  );
}

describe("useRowLink", () => {
  afterEach(() => vi.restoreAllMocks());

  it("pushes on a plain left click", () => {
    const push = vi.fn();
    renderProbe(makeAdapter({ push }));
    fireEvent.click(screen.getByRole("row"));
    expect(push).toHaveBeenCalledWith("/acme/projects/p1");
  });

  it("opens a shareable browser tab for modifier clicks", () => {
    const push = vi.fn();
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    renderProbe(
      makeAdapter({
        push,
        getShareableUrl: (path) => `https://app.example${path}`,
      }),
    );

    fireEvent.click(screen.getByRole("row"), { metaKey: true });
    fireEvent.click(screen.getByRole("row"), { ctrlKey: true });
    expect(open).toHaveBeenCalledTimes(2);
    expect(open).toHaveBeenNthCalledWith(
      1,
      "https://app.example/acme/projects/p1",
      "_blank",
      "noopener,noreferrer",
    );
    expect(push).not.toHaveBeenCalled();
  });

  it("opens a shareable browser tab and prevents default for middle clicks", () => {
    const push = vi.fn();
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    renderProbe(
      makeAdapter({
        push,
        getShareableUrl: (path) => `https://app.example${path}`,
      }),
    );

    const event = new MouseEvent("auxclick", {
      bubbles: true,
      button: 1,
      cancelable: true,
    });
    screen.getByRole("row").dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    expect(open).toHaveBeenCalledWith(
      "https://app.example/acme/projects/p1",
      "_blank",
      "noopener,noreferrer",
    );
    expect(push).not.toHaveBeenCalled();
  });

  it("ignores non-middle auxiliary buttons", () => {
    const push = vi.fn();
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    renderProbe(makeAdapter({ push }));
    const event = new MouseEvent("auxclick", {
      bubbles: true,
      button: 2,
      cancelable: true,
    });
    screen.getByRole("row").dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    expect(open).not.toHaveBeenCalled();
    expect(push).not.toHaveBeenCalled();
  });

  it("shields nested interactive elements from row handlers", () => {
    const push = vi.fn();
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const adapter = makeAdapter({ push });

    function NestedProbe() {
      const rowLink = useRowLink();
      return (
        <div role="row" {...rowLink("/acme/projects/p1")}>
          <a href="https://example.com" {...rowLinkInteractiveProps}>
            source
          </a>
        </div>
      );
    }

    render(
      <NavigationProvider value={adapter}>
        <NestedProbe />
      </NavigationProvider>,
    );
    const anchor = screen.getByRole("link");
    fireEvent.click(anchor);
    const event = new MouseEvent("auxclick", {
      bubbles: true,
      button: 1,
      cancelable: true,
    });
    anchor.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    expect(push).not.toHaveBeenCalled();
    expect(open).not.toHaveBeenCalled();
  });
});
