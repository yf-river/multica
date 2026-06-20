import { describe, expect, it } from "vitest";
import { prefixLocale } from "./locale-link";

describe("prefixLocale", () => {
  it("keeps Chinese-only root-relative paths prefix-free", () => {
    expect(prefixLocale("/workspaces", "zh")).toBe("/workspaces");
    expect(prefixLocale("/agents-create", "zh")).toBe("/agents-create");
  });

  it("preserves anchors and query strings", () => {
    expect(prefixLocale("/providers#claude-code", "zh")).toBe(
      "/providers#claude-code",
    );
    expect(prefixLocale("/agents?from=docs", "zh")).toBe(
      "/agents?from=docs",
    );
  });

  it("keeps the bare root path unchanged", () => {
    expect(prefixLocale("/", "zh")).toBe("/");
  });

  it("leaves external URLs alone", () => {
    expect(prefixLocale("https://multica.ai/download", "zh")).toBe(
      "https://multica.ai/download",
    );
    expect(prefixLocale("mailto:hello@multica.ai", "zh")).toBe(
      "mailto:hello@multica.ai",
    );
    expect(prefixLocale("tel:+1234567890", "zh")).toBe("tel:+1234567890");
  });

  it("leaves in-page anchors and relative paths alone", () => {
    expect(prefixLocale("#section", "zh")).toBe("#section");
    expect(prefixLocale("./sibling", "zh")).toBe("./sibling");
    expect(prefixLocale("../sibling", "zh")).toBe("../sibling");
  });

  it("returns empty/undefined hrefs unchanged", () => {
    expect(prefixLocale("", "zh")).toBe("");
  });
});
