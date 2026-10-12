import { describe, expect, it } from "vitest";
import { environmentCookieName } from "./environment";
describe("environment cookies", () => {
  it("separates all deployment cookies while retaining unnamed deployments", () => {
    for (const name of ["multica_auth", "multica_csrf", "multica_logged_in", "last_workspace_slug", "multica_signup_source"]) {
      expect(environmentCookieName(name)).toBe(name);
      expect(environmentCookieName(name, "int")).toBe(`${name}_int`);
      expect(environmentCookieName(name, "prod")).not.toBe(environmentCookieName(name, "int"));
    }
    expect(() => environmentCookieName("multica_auth", "int;path=/")).toThrow();
  });
});

describe("API CSRF selection", () => {
  it("selects the configured environment even when both cookies exist", async () => {
    const { ApiClient } = await import("../api/client");
    const { vi } = await import("vitest");
    const fetchMock = vi.fn().mockImplementation(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("document", { cookie: "multica_csrf=legacy; multica_csrf_prod=production; multica_csrf_int=integration" });
    try {
      await new ApiClient("", { environmentId: "int" }).logout();
      expect(fetchMock.mock.calls[0]?.[1]?.headers?.["X-CSRF-Token"]).toBe("integration");
      await new ApiClient("", { environmentId: "prod" }).logout();
      expect(fetchMock.mock.calls[1]?.[1]?.headers?.["X-CSRF-Token"]).toBe("production");
      vi.stubGlobal("document", { cookie: "multica_csrf=legacy; multica_csrf_prod=production" });
      await new ApiClient("", { environmentId: "int" }).logout();
      expect(fetchMock.mock.calls[2]?.[1]?.headers?.["X-CSRF-Token"]).toBeUndefined();
    } finally { vi.unstubAllGlobals(); }
  });
});
