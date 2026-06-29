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

  it("redirects old top-level training run history links to evaluation run records", () => {
    expect(
      redirectLocation("/training?view=run-history&run=run-123", {
        multica_logged_in: "1",
        last_workspace_slug: "team-a",
      }),
    ).toBe("http://localhost/team-a/training/evaluation-runs?run=run-123");
  });

  it("redirects old top-level optimization links to test suites with issue context", () => {
    expect(
      redirectLocation("/training?view=optimization-runs&issue=issue-1", {
        multica_logged_in: "1",
        last_workspace_slug: "team-a",
      }),
    ).toBe("http://localhost/team-a/training/test-suites?issue=issue-1&mode=optimize");
  });

  it("redirects old top-level training debug links to the combined debug route", () => {
    expect(
      redirectLocation("/training?view=agent-playground", {
        multica_logged_in: "1",
        last_workspace_slug: "team-a",
      }),
    ).toBe("http://localhost/team-a/training/debug-runs");
  });

  it("redirects evaluation aliases to run reviews", () => {
    for (const path of ["/evaluation", "/eval"]) {
      expect(
        redirectLocation(path, {
          multica_logged_in: "1",
          last_workspace_slug: "team-a",
        }),
      ).toBe("http://localhost/team-a/run-reviews");
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
      makeRequest("/team-a/training/debug-runs", {
        multica_logged_in: "1",
        last_workspace_slug: "team-a",
      }),
    );

    expect(res.headers.get("location")).toBeNull();
  });
});
