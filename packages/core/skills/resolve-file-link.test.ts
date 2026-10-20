// @vitest-environment node
import { describe, expect, it } from "vitest";
import { resolveSkillFileLink } from "./resolve-file-link";

const files = ["SKILL.md", "references/project-map.md", "references/workflow.md", "references/项目 图.md", "assets/sample.pdf"];

describe("resolveSkillFileLink", () => {
  it.each([
    ["SKILL.md", "references/project-map.md", "references/project-map.md"],
    ["SKILL.md", "./references/project-map.md#setup", "references/project-map.md"],
    ["references/project-map.md", "./workflow.md", "references/workflow.md"],
    ["references/project-map.md", "../SKILL.md", "SKILL.md"],
    ["SKILL.md", "references/%E9%A1%B9%E7%9B%AE%20%E5%9B%BE.md?raw=1#intro", "references/项目 图.md"],
    ["references/project-map.md", "../assets/sample.pdf", "assets/sample.pdf"],
  ])("resolves %s -> %s", (current, href, expected) => {
    expect(resolveSkillFileLink(current, href, files)).toBe(expected);
  });

  it.each(["https://example.org/guide.md", "//example.org/guide.md", "mailto:a@example.org", "mention://issue/123", "/references/project-map.md", "#intro", "?raw=1", "", "missing.md", "../SKILL.md", "%2fSKILL.md", "references/%ZZ.md", "references\\project-map.md"])("ignores non-file or invalid destinations: %s", href => {
    expect(resolveSkillFileLink("SKILL.md", href, files)).toBeNull();
  });
});
