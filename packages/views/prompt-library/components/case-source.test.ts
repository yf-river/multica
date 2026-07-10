import { describe, expect, it } from "vitest";
import { caseSourceLabel } from "./case-source";

describe("caseSourceLabel", () => {
  it.each([
    ["manual", "手工"],
    ["trace", "trace导入"],
    ["payload", "资产载荷"],
  ])("maps the persisted %s source to its current filter label", (source, label) => {
    expect(caseSourceLabel(source)).toBe(label);
  });

  it("keeps an explicit label for unknown external sources", () => {
    expect(caseSourceLabel("external")).toBe("导入");
  });
});
