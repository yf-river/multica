import { describe, expect, it } from "vitest";
import { matchLocale } from "./pick-locale";

describe("matchLocale", () => {
  it("always selects the only supported locale", () => {
    expect(matchLocale([])).toBe("zh-Hans");
    expect(matchLocale(["en-US"])).toBe("zh-Hans");
    expect(matchLocale(["zh-Hans"])).toBe("zh-Hans");
  });
});
