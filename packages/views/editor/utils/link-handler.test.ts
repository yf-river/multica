import { describe, expect, it, vi } from "vitest";

import { openLink } from "./link-handler";

describe("openLink", () => {
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
});
