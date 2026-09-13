// 本文件是“批量编辑 env”用到的 dotenv 风格文本解析/序列化工具：把粘贴进来的文本
// 解析成键值对（带行号报错），或把条目列表回写为可被同一解析器原样读回的文本。
// 只做解析不做求值，绝不改写值本身。

/** 一条环境变量：键 + 值。 */
export interface EnvAssignment {
  key: string;
  value: string;
}

/**
 * 为什么行号重要：一次粘贴或批量修改可能带几十行，只说“解析不了”而不给位置，
 * 用户只能瞎猜。`line` 从 1 开始计，并计入解析器跳过的空行和注释行，
 * 因此与编辑器里看到的一致。
 */
export type EnvParseError =
  | { kind: "malformed"; line: number }
  | { kind: "duplicate"; line: number; key: string };

/** 解析结果：成功给出全部条目；失败给出第一个坏行及原因。 */
export type EnvParseResult =
  | { ok: true; assignments: EnvAssignment[] }
  | { ok: false; error: EnvParseError };

const ASSIGNMENT_PATTERN =
  /^(?:export[\t ]+)?([A-Za-z_][A-Za-z0-9_]*)[\t ]*=(.*)$/;
const ASSIGNMENT_PREFIX_PATTERN =
  /^(?:export[\t ]+)?[A-Za-z_][A-Za-z0-9_]*[\t ]*=/;

/** 解析带引号的值（"..." 或 '...'）：找到收尾引号；若其后只剩空格/注释则接受，返回引号内文本。 */
function parseQuotedValue(rawValue: string): string | null {
  const quote = rawValue[0];
  if (quote !== '"' && quote !== "'") return null;

  for (let index = 1; index < rawValue.length; index += 1) {
    if (rawValue[index] === quote) {
      const remainder = rawValue.slice(index + 1);
      if (remainder.trim() === "" || /^[\t ]+#/.test(remainder)) {
        return rawValue.slice(1, index);
      }
    }
  }

  return null;
}

/** 解析无引号的值：# 前有空白（或行首）即视为注释起点，截掉注释并去尾空。 */
function parseUnquotedValue(
  rawValue: string,
  hadLeadingWhitespace: boolean,
): string {
  for (let index = 0; index < rawValue.length; index += 1) {
    const character = rawValue[index];

    if (
      character === "#" &&
      ((index === 0 && hadLeadingWhitespace) ||
        /\s/.test(rawValue[index - 1] ?? ""))
    ) {
      return rawValue.slice(0, index).trimEnd();
    }
  }

  return rawValue.trimEnd();
}

/** 值解析入口：引号开头走引号解析，否则走无引号解析。 */
function parseValue(rawValue: string): string | null {
  const hadLeadingWhitespace = /^[\t ]/.test(rawValue);
  const value = rawValue.trimStart();
  if (value.startsWith('"') || value.startsWith("'")) {
    return parseQuotedValue(value);
  }

  return parseUnquotedValue(value, hadLeadingWhitespace);
}

/** 判断一段文本是否像 env 文件粘贴内容：去掉空行/注释后，只要有一行长得像 “K=V” 就算。 */
export function isEnvFilePaste(text: string): boolean {
  const trimmedText = text.trim();
  if (!trimmedText) return false;

  const meaningfulLines = trimmedText
    .replace(/\r\n?/g, "\n")
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "" && !line.startsWith("#"));

  return meaningfulLines.some((line) => ASSIGNMENT_PREFIX_PATTERN.test(line));
}

/**
 * 解析 dotenv 风格文本，不求值、不改写值，报告第一条坏行所在位置。
 * 一个赋值都没有的输入是“成功 + 空列表”——这正是批量编辑表达“全清空”的方式。
 * 需要“这根本不算 env 文件”语义的调用方，用 `parseEnvFile`。
 */
export function parseEnvFileResult(text: string): EnvParseResult {
  const assignments: EnvAssignment[] = [];
  const keys = new Set<string>();
  const lines = text.replace(/\r\n?/g, "\n").split("\n");

  for (const [index, line] of lines.entries()) {
    const trimmedLine = line.trim();
    if (trimmedLine === "" || trimmedLine.startsWith("#")) continue;

    const lineNumber = index + 1;
    const match = ASSIGNMENT_PATTERN.exec(trimmedLine);
    if (!match) {
      return { ok: false, error: { kind: "malformed", line: lineNumber } };
    }

    const value = parseValue(match[2] ?? "");
    if (value === null) {
      return { ok: false, error: { kind: "malformed", line: lineNumber } };
    }

    const key = match[1] ?? "";
    if (keys.has(key)) {
      return { ok: false, error: { kind: "duplicate", line: lineNumber, key } };
    }

    keys.add(key);
    assignments.push({ key, value });
  }

  return { ok: true, assignments };
}

/** 解析一段粘贴的 dotenv 风格文本（不求值、不改写）。无有效条目返回 null。 */
export function parseEnvFile(text: string): EnvAssignment[] | null {
  const result = parseEnvFileResult(text);
  if (!result.ok || result.assignments.length === 0) return null;
  return result.assignments;
}

/** 序列化结果：成功给回文本；失败给出第一个无法表示的键及原因。 */
export type EnvFormatResult =
  | { ok: true; text: string }
  | { ok: false; reason: "unrepresentable" | "duplicate"; key: string };

const KEY_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;

/**
 * 用“回环测试”挑选编码方式：把每个候选值丢回将来要读它的同一个解析器，
 * 而不是自己去推理哪些字符危险——正是那种推理曾把 `foo" #bar` 变成 `foo`。
 *
 * 有些值根本没有可表示形态：值内换行（解析器先按行切分）、或在两种引号下都会
 * 提前闭合（如 `a" #b' #c`）。服务端接受这些值，所以宁可报错也不能改写成
 * “看着可编辑其实已变样”的东西。
 */
function encodeValue(key: string, value: string): string | null {
  if (/[\r\n]/.test(value)) return null;

  for (const candidate of [value, `"${value}"`, `'${value}'`]) {
    const result = parseEnvFileResult(`${key}=${candidate}`);
    if (result.ok && result.assignments[0]?.value === value) return candidate;
  }

  return null;
}

/**
 * 把条目列表序列化回 `parseEnvFileResult` 能一字不差读回的文本；做不到就指出
 * 第一个无法表示的条目，让调用方拒绝进入批量编辑，不碰任何数据。
 *
 * 成功意味着整段文本能往返一致，所以重复键是“拒绝”而不是“格式细节”：两个同键
 * 条目永远只能读回一个，任何文本都满足不了往返契约。
 */
export function formatEnvFile(assignments: EnvAssignment[]): EnvFormatResult {
  const lines: string[] = [];
  const keys = new Set<string>();

  for (const { key, value } of assignments) {
    if (!KEY_PATTERN.test(key)) {
      return { ok: false, reason: "unrepresentable", key };
    }
    if (keys.has(key)) return { ok: false, reason: "duplicate", key };

    const encoded = encodeValue(key, value);
    if (encoded === null) {
      return { ok: false, reason: "unrepresentable", key };
    }

    keys.add(key);
    lines.push(`${key}=${encoded}`);
  }

  return { ok: true, text: lines.join("\n") };
}
