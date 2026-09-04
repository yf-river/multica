// @vitest-environment node
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const chineseFonts = ["PingFang SC", "Microsoft YaHei", "Noto Sans CJK SC"];
function expectChineseFonts(source: string) {
  const chineseIndexes = chineseFonts.map((font) => source.indexOf(font));
  expect(chineseIndexes).not.toContain(-1);
}

describe("Chinese font fallbacks", () => {
  it("keeps the desktop Chinese font stack", () => {
    const desktopCss = readFileSync(
      resolve(process.cwd(), "src/renderer/src/globals.css"),
      "utf8",
    );

    expectChineseFonts(desktopCss);
    expect(desktopCss).not.toContain('html[lang|="ja"]');
    expect(desktopCss).not.toContain("Noto Sans CJK KR");
    expect(desktopCss).not.toContain("Noto Sans CJK JP");
  });
});
