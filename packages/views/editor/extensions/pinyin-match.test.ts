import { describe, it, expect } from "vitest";
import { matchesTextQuery } from "./pinyin-match";

describe("matchesTextQuery", () => {
  it.each([
    ["Alice", "ali"],
    ["Alice", "ALI"],
    ["李云龙", "lyl"],
    ["李云龙", "云龙"],
    ["李云龙", "liyunlong"],
    ["李云龙", "liyu"],
    ["李云龙", "liyunl"],
    ["李云龙", ""],
    ["张大彪", "z"],
    ["张大彪", "zdb"],
    ["张大彪", "zhangdabiao"],
    ["魏和尚", "whs"],
    ["魏和尚", "weiheshang"],
    ["吕布", "lvbu"],
    ["吕布", "lb"],
    ["吕布", "lv"],
  ])("matches %s with %s", (text, query) => {
    expect(matchesTextQuery(text, query)).toBe(true);
  });

  it("rejects an unrelated pinyin query", () => {
    expect(matchesTextQuery("李云龙", "zhangsan")).toBe(false);
  });
});
