import { describe, it, expect } from "vitest";
import { matchesPinyin } from "./pinyin-match";

describe("matchesPinyin", () => {
  it.each([
    ["李云龙", "liyunlong"],
    ["李云龙", "lyl"],
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
  ])("matches %s with %s", (name, query) => {
    expect(matchesPinyin(name, query)).toBe(true);
  });

  it.each([
    ["李云龙", "zhangsan"],
    ["Alice", "ali"],
  ])("does not match %s with %s", (name, query) => {
    expect(matchesPinyin(name, query)).toBe(false);
  });
});
