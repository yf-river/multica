import { describe, expect, it } from "vitest";
import type { Issue } from "@multica/core/types";
import { sortIssues } from "./sort";

const propertyId = "prop-effort";

function issueWith(id: string, value?: number | string, position = 0): Issue {
  return {
    id,
    position,
    properties: value === undefined ? {} : { [propertyId]: value },
  } as unknown as Issue;
}

describe("sortIssues property sorts", () => {
  it("sorts number values numerically, missing values last", () => {
    const sorted = sortIssues(
      [issueWith("big", 10), issueWith("not_important_not_urgent"), issueWith("small", 2)],
      `property:${propertyId}`,
      "asc",
    );
    expect(sorted.map((i) => i.id)).toEqual(["small", "big", "not_important_not_urgent"]);
  });

  it("desc reverses values but keeps missing values last", () => {
    const sorted = sortIssues(
      [issueWith("not_important_not_urgent"), issueWith("small", 2), issueWith("big", 10)],
      `property:${propertyId}`,
      "desc",
    );
    expect(sorted.map((i) => i.id)).toEqual(["big", "small", "not_important_not_urgent"]);
  });

  it("sorts date-only strings chronologically via lexical compare", () => {
    const sorted = sortIssues(
      [issueWith("later", "2026-08-01"), issueWith("earlier", "2026-07-13")],
      `property:${propertyId}`,
      "asc",
    );
    expect(sorted.map((i) => i.id)).toEqual(["earlier", "later"]);
  });

  it("falls back to position order for the static fields", () => {
    const sorted = sortIssues(
      [issueWith("b", undefined, 2), issueWith("a", undefined, 1)],
      "position",
      "asc",
    );
    expect(sorted.map((i) => i.id)).toEqual(["a", "b"]);
  });
});
