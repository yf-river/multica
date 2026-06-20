import { describe, expect, it } from "vitest";
import { docsSlugStaticParams } from "./static-params";

type RawParam = { lang: string; slug: string[] };

describe("docsSlugStaticParams", () => {
  it("returns Chinese slug pages and drops the home param", () => {
    const params: RawParam[] = [
      { lang: "zh", slug: [] },
      { lang: "zh", slug: ["agents"] },
      { lang: "zh", slug: ["cli", "reference"] },
      { lang: "fr", slug: ["agents"] },
    ];

    expect(docsSlugStaticParams(params)).toEqual([
      { lang: "zh", slug: ["agents"] },
      { lang: "zh", slug: ["cli", "reference"] },
    ]);
  });

  it("de-duplicates repeated params", () => {
    const params: RawParam[] = [
      { lang: "zh", slug: ["agents"] },
      { lang: "zh", slug: ["agents"] },
    ];

    expect(docsSlugStaticParams(params)).toEqual([
      { lang: "zh", slug: ["agents"] },
    ]);
  });
});
