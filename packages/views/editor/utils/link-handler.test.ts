import { describe, expect, it, vi } from "vitest";

import { openLink } from "./link-handler";

describe("openLink", () => {
  it.each(["/prompt-library", "/evaluation", "/eval"])(
    "canonicalizes legacy training link %s to training prompts",
    (href) => {
    const listener = vi.fn();
    window.addEventListener("multica:navigate", listener);

    openLink(href, "acme");

    expect(listener).toHaveBeenCalledTimes(1);
    expect(listener.mock.calls[0]?.[0]).toMatchObject({
      detail: { path: "/acme/training?view=prompts" },
    });

    window.removeEventListener("multica:navigate", listener);
    },
  );
});
