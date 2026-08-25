import { describe, it, expect } from "vitest";
import { escapeMarkdownLabel } from "./escape-markdown-label";

describe("escapeMarkdownLabel", () => {
  it.each([
    ["photo[1].png", "photo\\[1\\].png"],
    ["a\\b", "a\\\\b"],
    ["file(1).txt", "file\\(1\\).txt"],
    ["6P4N\\`X[A~Z(S@XO}WE0FT_P.jpg", "6P4N\\\\`X\\[A~Z\\(S@XO}WE0FT_P.jpg"],
    ["hello world.png", "hello world.png"],
  ])("escapes %s", (input, expected) => {
    expect(escapeMarkdownLabel(input)).toBe(expected);
  });
});
