import { describe, expect, it, vi } from "vitest";

import { openLink } from "./link-handler";

describe("openLink", () => {
  it.each([
    ["/prompt-library", "/acme/training/prompts"],
    ["/evaluation", "/acme/training/runs"],
    ["/eval", "/acme/training/runs"],
  ])(
    "canonicalizes legacy training link %s to a semantic training route",
    (href, path) => {
    const listener = vi.fn();
    window.addEventListener("multica:navigate", listener);

    openLink(href, "acme");

    expect(listener).toHaveBeenCalledTimes(1);
    expect(listener.mock.calls[0]?.[0]).toMatchObject({
      detail: { path },
    });

    window.removeEventListener("multica:navigate", listener);
    },
  );
});
