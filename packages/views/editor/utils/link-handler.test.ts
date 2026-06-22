import { describe, expect, it, vi } from "vitest";

import { openLink } from "./link-handler";

describe("openLink", () => {
  it("canonicalizes legacy prompt library links to training prompts", () => {
    const listener = vi.fn();
    window.addEventListener("multica:navigate", listener);

    openLink("/prompt-library", "acme");

    expect(listener).toHaveBeenCalledTimes(1);
    expect(listener.mock.calls[0]?.[0]).toMatchObject({
      detail: { path: "/acme/training?view=prompts" },
    });

    window.removeEventListener("multica:navigate", listener);
  });
});
