// @vitest-environment node
import { describe, expect, it } from "vitest";
import { codeDiffFromResult, codeDiffRoots, textChangesFromResult } from "./code-diff";

describe("codeDiffFromResult", () => {
  it("accepts a persisted snapshot and ignores newer fields", () => {
    const code_diff = { before: "abc", after: "def", files: [{ path: "app.ts", added: 1, deleted: 2, binary: false, truncated: false, patch: "+new", future: true }], truncated: false, unavailable: false };
    expect(codeDiffFromResult({ code_diff })?.files[0]?.path).toBe("app.ts");
  });
  it.each([null, "output", {}, { code_diff: null }, { code_diff: { files: "invalid" } }, { code_diff: { before: "a", after: "b", files: [{ path: "x", added: -1 }], truncated: false, unavailable: false } }])("hides malformed or older results: %j", (result) => {
    expect(codeDiffFromResult(result)).toBeNull();
  });
});

describe("platform text and roots", () => {
  it("reads prompt edits recorded beside the file diff", () => {
    const changes = textChangesFromResult({ text_changes: [{ target_kind: "skill", target_id: "s1", field: "content", added: 2, deleted: 1 }] });
    expect(changes).toHaveLength(1);
    expect(changes[0]?.target_kind).toBe("skill");
  });
  it("wraps a v1 flat result in a single work-directory root", () => {
    const diff = codeDiffFromResult({ code_diff: { before: "a", after: "b", truncated: false, unavailable: false, files: [{ path: "x.ts", added: 1, deleted: 0, binary: false, truncated: false, patch: "+x" }] } });
    expect(diff && codeDiffRoots(diff)).toHaveLength(1);
    expect(diff && codeDiffRoots(diff)[0]?.files[0]?.path).toBe("x.ts");
  });
  it("keeps v2 roots apart", () => {
    const diff = codeDiffFromResult({ code_diff: { before: "", after: "", truncated: false, unavailable: false, version: 2, files: [], roots: [
      { path: "/w", kind: "workdir", label: "任务目录", files: [] },
      { path: "/e", kind: "external", label: "任务目录外的工作树", attribution: "unassigned", files: [] },
    ] } });
    expect(diff && codeDiffRoots(diff)).toHaveLength(2);
    expect(diff && codeDiffRoots(diff)[1]?.attribution).toBe("unassigned");
  });
});
