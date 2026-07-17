import { describe, expect, it } from "vitest";
import { parseCurrentContextRoute } from "./use-chat-context-items";

describe("parseCurrentContextRoute", () => {
  it.each([
    ["/acme/issues/issue-1", "", { type: "issue", id: "issue-1" }],
    ["/acme/projects/project-1", "", { type: "project", id: "project-1" }],
    ["/acme/inbox", "issue=issue-42", { type: "issue", id: "issue-42" }],
  ] as const)("parses current context from %s", (path, query, expected) => {
    expect(parseCurrentContextRoute(path, new URLSearchParams(query))).toEqual(expected);
  });

  it("does not treat the bare inbox route as current issue context", () => {
    expect(parseCurrentContextRoute("/acme/inbox", new URLSearchParams())).toBeNull();
  });
});
