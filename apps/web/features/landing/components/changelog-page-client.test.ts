import { describe, expect, it } from "vitest";
import { fullDateLabel, monthYearLabel } from "./changelog-page-client";

describe("changelog date labels", () => {
  it("formats month labels in Chinese", () => {
    expect(monthYearLabel(2026, 1, "zh-Hans")).toBe("2026年1月");
  });

  it("formats full dates in Chinese", () => {
    expect(fullDateLabel("2026-01-15", "zh-Hans")).toBe("2026年1月15日");
  });

  it("keeps invalid release dates unchanged", () => {
    expect(fullDateLabel("not-a-date", "zh-Hans")).toBe("not-a-date");
  });
});
