// @vitest-environment node
import { describe, expect, it } from "vitest";
import { docsHrefForLocale } from "./docs-href";

describe("docsHrefForLocale", () => {
  it("routes the Chinese product to its docs entry", () => {
    expect(docsHrefForLocale("zh-Hans")).toBe("/docs");
  });
});
