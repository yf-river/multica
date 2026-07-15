import { describe, it, expect } from "vitest";
import { matchHighlightAt, findLiteralRanges } from "./highlight-match";

/** Convenience: match at the first `==` and return the inner text, or null. */
function inner(text: string): string | null {
  const i = text.indexOf("==");
  if (i === -1) return null;
  const m = matchHighlightAt(text, i);
  return m ? m.inner : null;
}

describe("matchHighlightAt", () => {
  it("matches a basic highlight at the opening fence", () => {
    const m = matchHighlightAt("==hi==", 0);
    expect(m).toEqual({ end: 6, inner: "hi" });
  });

  it.each([
    ["== x ==", null],
    ["====", null],
    ["==a `b==c` d==", "a `b==c` d"],
    ["==a $b==c$ d==", "a $b==c$ d"],
    ["`x==y==z`", null],
    ["==a\n\nb==", null],
    ["==a\nb==", "a\nb"],
    ["==a\r\n\r\nb==", null],
    ["==a\r\nb==", "a\r\nb"],
  ])("matches %j with current fence rules", (input, expected) => {
    expect(inner(input)).toBe(expected);
  });

  it("returns null when the opening fence is not at i", () => {
    expect(matchHighlightAt("x==hi==", 0)).toBeNull();
  });

  it("matches the nearest valid closing fence", () => {
    // first valid close wins; trailing == is left over
    expect(matchHighlightAt("==a==b==", 0)).toEqual({ end: 5, inner: "a" });
  });
});

describe("findLiteralRanges", () => {
  it("treats == inside a fenced block as literal", () => {
    const text = "```\n==x==\n```";
    const ranges = findLiteralRanges(text);
    const fencePos = text.indexOf("==x==");
    expect(ranges.some((r) => fencePos >= r.start && fencePos < r.end)).toBe(true);
  });
});
