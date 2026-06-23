import { NextRequest } from "next/server";
import { describe, expect, it } from "vitest";

import { proxy } from "./proxy";

function makeRequest(path: string, cookies: Record<string, string> = {}) {
  const req = new NextRequest(new URL(path, "http://localhost"));
  for (const [name, value] of Object.entries(cookies)) {
    req.cookies.set(name, value);
  }
  return req;
}

function redirectLocation(path: string, cookies: Record<string, string> = {}) {
  return proxy(makeRequest(path, cookies)).headers.get("location");
}

describe("web proxy legacy route compatibility", () => {
  it("redirects old prompt-library links to the Chinese training prompts view", () => {
    expect(
      redirectLocation("/prompt-library?source=old-bookmark", {
        multica_logged_in: "1",
        last_workspace_slug: "team-a",
      }),
    ).toBe("http://localhost/team-a/training/prompts?source=old-bookmark");
  });

  it("redirects old top-level training deep links into the last workspace", () => {
    expect(
      redirectLocation("/training?view=run-history", {
        multica_logged_in: "1",
        last_workspace_slug: "team-a",
      }),
    ).toBe("http://localhost/team-a/training/run-history");
  });

  it("redirects evaluation aliases to the Chinese training dashboard", () => {
    for (const path of ["/evaluation", "/eval"]) {
      expect(
        redirectLocation(path, {
          multica_logged_in: "1",
          last_workspace_slug: "team-a",
        }),
      ).toBe("http://localhost/team-a/training/runs");
    }
  });

  it("sends logged-out legacy routes to login instead of a 404", () => {
    expect(redirectLocation("/prompt-library")).toBe("http://localhost/login");
  });

  it("uses root redirect to restore the latest workspace for logged-in users", () => {
    expect(
      redirectLocation("/", {
        multica_logged_in: "1",
        last_workspace_slug: "team-a",
      }),
    ).toBe("http://localhost/team-a/issues");
  });

  it("does not redirect canonical workspace-scoped training URLs", () => {
    const res = proxy(
      makeRequest("/team-a/training/experiments", {
        multica_logged_in: "1",
        last_workspace_slug: "team-a",
      }),
    );

    expect(res.headers.get("location")).toBeNull();
  });
});
