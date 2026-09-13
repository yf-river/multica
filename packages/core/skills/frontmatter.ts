// 本文件提供技能文件 frontmatter 的解析：把 `---\n...\n---` 开头的 YAML 头
// 与正文分开，并把标量值归一成字符串。

import { parse as parseYaml } from "yaml";

// 把结尾换行留进捕获组很关键：yaml 的 `|` clip 消除模式只有在输入本身带
// 换行时才保留末换行。与 Go 侧 `server/internal/skill` 用的正则保持一致。
const FRONTMATTER_RE = /^---\r?\n([\s\S]*?\r?\n)---\r?\n?/;

/** 技能 frontmatter：键 → 字符串值。 */
export type SkillFrontmatter = Record<string, string>;

/** 解析结果：frontmatter（可能没有）与正文。 */
export interface ParsedFrontmatter {
  frontmatter: SkillFrontmatter | null;
  body: string;
}

/**
 * 从原始文件文本里解析 frontmatter 与正文。
 *
 * @param raw - 完整文件内容
 * @returns 解析结果；没有合法的 frontmatter 时 frontmatter 为 null、body 原样
 */
export function parseFrontmatter(raw: string): ParsedFrontmatter {
  const match = FRONTMATTER_RE.exec(raw);
  if (!match) return { frontmatter: null, body: raw };

  const yamlBlock = match[1]!;
  const body = raw.slice(match[0].length);

  let parsed: unknown;
  try {
    parsed = parseYaml(yamlBlock);
  } catch {
    return { frontmatter: null, body };
  }

  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    return { frontmatter: null, body };
  }

  const result: SkillFrontmatter = {};
  for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
    if (value == null) continue;
    if (typeof value === "string") {
      result[key] = value;
    } else if (typeof value === "number" || typeof value === "boolean") {
      result[key] = String(value);
    } else {
      result[key] = JSON.stringify(value);
    }
  }

  return {
    frontmatter: Object.keys(result).length > 0 ? result : null,
    body,
  };
}
