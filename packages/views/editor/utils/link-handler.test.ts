import { afterEach, describe, expect, it, vi } from "vitest";

import { openLink } from "./link-handler";

describe("openLink", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("prepends the workspace slug for current workspace-scoped links", () => {
    const listener = vi.fn();
    window.addEventListener("multica:navigate", listener);

    openLink("/issues/abc", "acme");

    expect(listener).toHaveBeenCalledTimes(1);
    expect(listener.mock.calls[0]?.[0]).toMatchObject({
      detail: { path: "/acme/issues/abc" },
    });

    window.removeEventListener("multica:navigate", listener);
  });

  it("opens safe external links in an isolated tab", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);

    openLink("https://example.com/docs");

    expect(open).toHaveBeenCalledWith(
      "https://example.com/docs",
      "_blank",
      "noopener,noreferrer",
    );
  });

  it("does not open executable or malformed URLs", () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);

    openLink("javascript:alert(document.cookie)");
    openLink("not a URL");

    expect(open).not.toHaveBeenCalled();
  });
});
