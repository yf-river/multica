import { afterEach, describe, expect, it, vi } from "vitest";

import { browserTimezone, resetBrowserTimezoneCache } from "./timezone-select";

describe("browserTimezone", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    resetBrowserTimezoneCache();
  });

  it("falls back to UTC when the runtime reports an invalid timezone", () => {
    vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
      locale: "en-US",
      calendar: "gregory",
      numberingSystem: "latn",
      timeZone: "Etc/Unknown",
    });

    expect(browserTimezone()).toBe("UTC");
  });
});
