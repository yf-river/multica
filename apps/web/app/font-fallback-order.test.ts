// @vitest-environment node
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const repoRoot = resolve(process.cwd(), "../..");
const chineseFonts = ["PingFang SC", "Microsoft YaHei", "Noto Sans CJK SC"];

function expectChineseFonts(source: string) {
  const chineseIndexes = chineseFonts.map((font) => source.indexOf(font));
  expect(chineseIndexes).not.toContain(-1);
}

describe("Chinese font fallback", () => {
  it("keeps the complete Chinese font stack", () => {
    const cssSource = readFileSync(
      resolve(repoRoot, "apps/web/app/globals.css"),
      "utf8",
    );

    expectChineseFonts(cssSource);
  });
});
