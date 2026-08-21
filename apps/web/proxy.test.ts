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

describe("web proxy", () => {
  it("uses root redirect to restore the latest workspace for logged-in users", () => {
    expect(
      redirectLocation("/", {
        multica_logged_in: "1",
        last_workspace_slug: "team-a",
      }),
    ).toBe("http://localhost/team-a/issues");
  });

  it("does not redirect canonical workspace-scoped debug and evaluation URLs", () => {
    const res = proxy(
      makeRequest("/team-a/debug/prompts", {
        multica_logged_in: "1",
        last_workspace_slug: "team-a",
      }),
    );

    expect(res.headers.get("location")).toBeNull();

    const evaluationRes = proxy(
      makeRequest("/team-a/evaluation/datasets", {
        multica_logged_in: "1",
        last_workspace_slug: "team-a",
      }),
    );

    expect(evaluationRes.headers.get("location")).toBeNull();
  });
});
