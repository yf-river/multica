// @vitest-environment node
import { describe, expect, it } from "vitest";
import { createRendererWebPreferences } from "./renderer-web-preferences";

const PRELOAD = "/app/out/preload/index.js";

describe("createRendererWebPreferences", () => {
  it("enables the Chromium process sandbox", () => {
    expect(createRendererWebPreferences(PRELOAD).sandbox).toBe(true);
  });

  it("keeps webSecurity off only until the privileged-protocol migration lands", () => {
    // Pinned deliberately: webSecurity must not come back on by accident.
    // Restoring it requires moving the renderer off the opaque file:// origin
    // onto a custom privileged protocol (so CORS preflights carry a real
    // Origin), plus server-side CORS coordination. Any change to this
    // expectation should be that migration, in its own PR.
    expect(createRendererWebPreferences(PRELOAD).webSecurity).toBe(false);
  });

  it("leaves contextIsolation and nodeIntegration at Electron's secure defaults", () => {
    // Not setting them means contextIsolation: true and nodeIntegration:
    // false (Electron defaults). The guard is that nothing re-enables the
    // insecure side of either default.
    const prefs = createRendererWebPreferences(PRELOAD);
    expect(prefs.contextIsolation).not.toBe(false);
    expect(prefs.nodeIntegration).not.toBe(true);
  });

  it("keeps plugins enabled for the PDF preview iframe", () => {
    expect(createRendererWebPreferences(PRELOAD).plugins).toBe(true);
  });

  it("loads the given preload script", () => {
    expect(createRendererWebPreferences(PRELOAD).preload).toBe(PRELOAD);
  });

  it("passes extra arguments to the renderer", () => {
    const prefs = createRendererWebPreferences(PRELOAD, [
      "--issue-window=<ctx>",
    ]);
    expect(prefs.additionalArguments).toEqual(["--issue-window=<ctx>"]);
  });

  it("defaults to no additional arguments", () => {
    expect(createRendererWebPreferences(PRELOAD).additionalArguments).toEqual([]);
  });
});
