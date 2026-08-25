import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const repoRoot = resolve(process.cwd(), "../..");
const chineseFonts = ["PingFang SC", "Microsoft YaHei", "Noto Sans CJK SC"];
const koreanFonts = ["Apple SD Gothic Neo", "Malgun Gothic", "Noto Sans CJK KR"];

function expectChineseFontsBeforeKoreanFonts(source: string) {
  const chineseIndexes = chineseFonts.map((font) => source.indexOf(font));
  const koreanIndexes = koreanFonts.map((font) => source.indexOf(font));

  expect(chineseIndexes).not.toContain(-1);
  expect(koreanIndexes).not.toContain(-1);

  for (const chineseIndex of chineseIndexes) {
    for (const koreanIndex of koreanIndexes) {
      expect(chineseIndex).toBeLessThan(koreanIndex);
    }
  }
}

describe("CJK font fallback order", () => {
  it.each([
    "apps/web/app/globals.css",
    "apps/docs/app/global.css",
  ])("keeps Chinese fallbacks before Korean fallbacks in %s", (path) => {
    expectChineseFontsBeforeKoreanFonts(readFileSync(resolve(repoRoot, path), "utf8"));
  });
});
