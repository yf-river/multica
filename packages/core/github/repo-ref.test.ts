// @vitest-environment node
import { describe, it, expect } from "vitest";
import {
  GIT_REF_MAX_LENGTH,
  validateGitRef,
} from "./repo-ref";

describe("validateGitRef", () => {
  it("accepts an empty ref as 'use the default branch'", () => {
    expect(validateGitRef("")).toEqual({ ok: true });
    expect(validateGitRef("   ")).toEqual({ ok: true });
  });

  it.each([
    ["a plain branch", "main"],
    ["a slashed branch", "release/2026-09"],
    ["a deeply slashed branch", "team/alice/feat/new-thing"],
    ["a semver tag", "v1.2.3"],
    ["a short SHA", "a1b2c3d"],
    ["a full SHA", "5e0b1cfa0a7d6a1a0f4b3f2e1d0c9b8a7f6e5d4c"],
    ["a branch with dots inside", "release.2026.09"],
    ["a branch with a hyphenated tail", "fix/MUL-7504-pin-ref"],
  ])("accepts %s", (_label, ref) => {
    expect(validateGitRef(ref)).toEqual({ ok: true });
  });

  it.each([
    ["a space", "my branch"],
    ["a tilde", "main~1"],
    ["a caret", "main^"],
    ["a colon", "origin:main"],
    ["a question mark", "main?"],
    ["an asterisk", "refs/heads/*"],
    ["a backslash", "feat\\thing"],
    ["an interior control character", "ma\nin"],
  ])("rejects %s", (_label, ref) => {
    expect(validateGitRef(ref)).toEqual({ ok: false, reason: "invalid_characters" });
  });

  it.each([
    ["a range", "main..dev"],
    ["reflog syntax", "main@{1}"],
    ["a lone at-sign", "@"],
    ["a leading slash", "/main"],
    ["a trailing slash", "main/"],
    ["a doubled slash", "feat//thing"],
    ["a leading dot", ".hidden"],
    ["a trailing dot", "main."],
    ["a lock suffix", "main.lock"],
    ["a lock suffix on an inner segment", "feat.lock/thing"],
    ["a dot-prefixed inner segment", "feat/.hidden"],
  ])("rejects %s", (_label, ref) => {
    expect(validateGitRef(ref)).toEqual({ ok: false, reason: "invalid_format" });
  });

  it("rejects a ref past the length cap but accepts one exactly at it", () => {
    expect(validateGitRef("a".repeat(GIT_REF_MAX_LENGTH))).toEqual({ ok: true });
    expect(validateGitRef("a".repeat(GIT_REF_MAX_LENGTH + 1))).toEqual({
      ok: false,
      reason: "too_long",
    });
  });
});


it("rejects option syntax and excessive UTF-8 path bytes", () => {
 expect(validateGitRef("--help").ok).toBe(false);
 expect(validateGitRef("中".repeat(86)).ok).toBe(false);
});
