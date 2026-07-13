import { describe, it, expect } from "vitest";
import { paths, isGlobalPath } from "./paths";
import { RESERVED_SLUGS } from "./reserved-slugs";

// C4 — current workspace paths always include their workspace slug.
describe("paths.workspace() shape", () => {
  it("exposes the expected parameterless workspace route methods", () => {
    const ws = paths.workspace("__probe__");
    const parameterlessRoutes = Object.entries(ws)
      .filter(([, fn]) => typeof fn === "function" && fn.length === 0)
      .map(([key]) => key);

    expect(new Set(parameterlessRoutes)).toEqual(
      new Set([
        "root",
        "usage",
        "runReviews",
        "issues",
        "projects",
        "autopilots",
        "agents",
        "squads",
        "inbox",
        "myIssues",
        "runtimes",
        "debug",
        "evaluation",
        "skills",
        "settings",
      ]),
    );
  });

  it("each parameterless route emits /{slug}/{segment}", () => {
    const ws = paths.workspace("acme");
    // Check that none of the parameterless paths embed a leaked literal
    // and that their second URL segment matches the method name's kebab-case.
    const expectedSegments: Array<[string, string]> = [
      ["usage", "usage"],
      ["runReviews", "run-reviews"],
      ["issues", "issues"],
      ["projects", "projects"],
      ["autopilots", "autopilots"],
      ["agents", "agents"],
      ["squads", "squads"],
      ["inbox", "inbox"],
      ["myIssues", "my-issues"],
      ["runtimes", "runtimes"],
      ["debug", "debug"],
      ["evaluation", "evaluation"],
      ["skills", "skills"],
      ["settings", "settings"],
    ];
    const wsAsAny = ws as unknown as Record<string, () => string>;
    for (const [method, segment] of expectedSegments) {
      const fn = wsAsAny[method];
      expect(typeof fn).toBe("function");
      expect(fn!()).toBe(`/acme/${segment}`);
    }
  });
});

// C5 — invariants between the global/reserved lists.
describe("global path / reserved slug consistency", () => {
  // If a path is "global" (never workspace-scoped), the slug name underlying it
  // must be reserved — otherwise a user could create a workspace with that slug
  // and shadow the global route's URL space.
  //
  const globalPaths = [paths.login(), paths.newWorkspace()];

  it("isGlobalPath agrees with the current global destinations", () => {
    for (const path of globalPaths) {
      expect(isGlobalPath(path)).toBe(true);
    }
    expect(isGlobalPath("/acme/issues")).toBe(false);
    expect(isGlobalPath("/")).toBe(false);
  });

  it("every global prefix's first path segment is a reserved slug", () => {
    for (const path of globalPaths) {
      const firstSegment = path.split("/").filter(Boolean)[0];
      if (!firstSegment) continue;
      expect(
        RESERVED_SLUGS.has(firstSegment),
        `'${firstSegment}' is a global path prefix but not a reserved slug — ` +
          `a workspace could be created with this slug and shadow the global route`,
      ).toBe(true);
    }
  });

  it("reserves canonical workspace route segments", () => {
    expect(RESERVED_SLUGS.has("debug")).toBe(true);
    expect(RESERVED_SLUGS.has("evaluation")).toBe(true);
  });
});
