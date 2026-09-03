import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { AppLink } from "./app-link";
import { NavigationProvider } from "./context";
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

function renderLink(
  adapter: NavigationAdapter,
  props: React.ComponentProps<typeof AppLink> = { href: "/issues" },
) {
  return render(
    <NavigationProvider value={adapter}>
      <AppLink {...props}>go</AppLink>
    </NavigationProvider>,
  );
}

function auxClick(element: HTMLElement, button = 1) {
  const event = new MouseEvent("auxclick", {
    bubbles: true,
    button,
    cancelable: true,
  });
  element.dispatchEvent(event);
  return event;
}

describe("AppLink", () => {
  it("keeps anchors route-relative for native browser navigation", () => {
    const adapter = makeAdapter({
      getShareableUrl: (path) => `https://app.example${path}`,
    });

    renderLink(adapter, { href: "/acme/issues/MUL-7" });
    expect(screen.getByRole("link", { name: "go" })).toHaveAttribute(
      "href",
      "/acme/issues/MUL-7",
    );
  });

  it("runs the caller before an in-place push", () => {
    const order: string[] = [];
    const adapter = makeAdapter({
      push: vi.fn(() => order.push("push")),
    });
    renderLink(adapter, {
      href: "/issues",
      onClick: () => order.push("onClick"),
    });

    fireEvent.click(screen.getByText("go"));
    expect(order).toEqual(["onClick", "push"]);
  });

  it("prefetches on hover and focus without replacing caller handlers", () => {
    const prefetch = vi.fn();
    const callerMouseEnter = vi.fn();
    const callerFocus = vi.fn();
    renderLink(makeAdapter({ prefetch }), {
      href: "/issues",
      onMouseEnter: callerMouseEnter,
      onFocus: callerFocus,
    });

    fireEvent.mouseEnter(screen.getByText("go"));
    fireEvent.focus(screen.getByText("go"));
    expect(prefetch).toHaveBeenNthCalledWith(1, "/issues");
    expect(prefetch).toHaveBeenNthCalledWith(2, "/issues");
    expect(callerMouseEnter).toHaveBeenCalledTimes(1);
    expect(callerFocus).toHaveBeenCalledTimes(1);
  });

  it("does not require optional prefetch", () => {
    renderLink(makeAdapter());
    expect(() => fireEvent.mouseEnter(screen.getByText("go"))).not.toThrow();
    expect(() => fireEvent.focus(screen.getByText("go"))).not.toThrow();
  });

  it("leaves modifier clicks to the browser and does not push", () => {
    const push = vi.fn();
    renderLink(makeAdapter({ push }));
    expect(fireEvent.click(screen.getByText("go"), { metaKey: true })).toBe(
      true,
    );
    expect(push).not.toHaveBeenCalled();
  });

  it("leaves shift clicks to the browser and does not push", () => {
    const push = vi.fn();
    renderLink(makeAdapter({ push }));
    expect(fireEvent.click(screen.getByText("go"), { shiftKey: true })).toBe(
      true,
    );
    expect(push).not.toHaveBeenCalled();
  });

  it("uses native target=_blank behavior and adds noopener", () => {
    renderLink(makeAdapter(), { href: "/issues", target: "_blank" });
    const link = screen.getByRole("link", { name: "go" });
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(fireEvent.click(link)).toBe(true);
  });

  it("does not intercept middle clicks", () => {
    const push = vi.fn();
    renderLink(makeAdapter({ push }));
    const event = auxClick(screen.getByText("go"));
    expect(event.defaultPrevented).toBe(false);
    expect(push).not.toHaveBeenCalled();
  });

  it("ignores non-middle auxiliary buttons", () => {
    const push = vi.fn();
    renderLink(makeAdapter({ push }));
    const event = auxClick(screen.getByText("go"), 2);
    expect(event.defaultPrevented).toBe(false);
    expect(push).not.toHaveBeenCalled();
  });

  it("lets callers cancel navigation", () => {
    const push = vi.fn();
    renderLink(makeAdapter({ push }), {
      href: "/issues",
      onClick: (event) => event.preventDefault(),
    });
    expect(fireEvent.click(screen.getByText("go"))).toBe(false);
    expect(push).not.toHaveBeenCalled();
  });

  it("runs the caller's auxiliary handler", () => {
    const onAuxClick = vi.fn();
    renderLink(makeAdapter(), { href: "/issues", onAuxClick });
    auxClick(screen.getByText("go"));
    expect(onAuxClick).toHaveBeenCalledTimes(1);
  });
});
