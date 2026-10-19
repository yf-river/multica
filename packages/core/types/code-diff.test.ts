// @vitest-environment node
import { describe, expect, it } from "vitest";
import { codeDiffFromResult } from "./code-diff";

describe("codeDiffFromResult", () => {
  it("accepts a persisted snapshot and ignores newer fields", () => {
    const code_diff = { before: "abc", after: "def", files: [{ path: "app.ts", added: 1, deleted: 2, binary: false, truncated: false, patch: "+new", future: true }], truncated: false, unavailable: false };
    expect(codeDiffFromResult({ code_diff })?.files[0]?.path).toBe("app.ts");
  });
  it.each([null, "output", {}, { code_diff: null }, { code_diff: { files: "invalid" } }, { code_diff: { before: "a", after: "b", files: [{ path: "x", added: -1 }], truncated: false, unavailable: false } }])("hides malformed or older results: %j", (result) => {
    expect(codeDiffFromResult(result)).toBeNull();
  });
});
