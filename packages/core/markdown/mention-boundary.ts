/** 判断 @ 前是否为提及边界：支持中文连续输入和全角空格，避免把邮箱当成提及。
 * 汉字邮箱与连续中文提及无法仅凭字符区分，中文连续输入按提及处理。
 */

/** Unicode word characters. An `@` glued to one of these continues a word. */
const WORD_CHARACTER = /[\p{L}\p{N}\p{M}_]/u;

/**
 * Scripts that do not separate words with spaces, where a word can therefore
 * end directly against the `@`. Latin, Cyrillic, Greek and friends are absent
 * on purpose: their writers type a space before `@`, and that absence is also
 * what keeps their email addresses from opening the picker.
 *
 * Written with `Script=`, not `Script_Extensions=`, because Hermes — the JS
 * engine the mobile app runs on — rejects `\p{Script_Extensions=Thai}` (also
 * Lao, Khmer and Tibetan) as an invalid property name and fails to parse the
 * module, which would take the app down with it. The escapes trailing the
 * scripts are the code points that carry one of these scripts only as an
 * extension *and* are word characters, so a `Script=` test alone would read
 * them as word continuations: U+3099/U+309A and U+FF9E/U+FF9F voiced sound
 * marks, and U+30FC/U+FF70 prolonged sound marks — the last character of
 * `コーヒー`, which would otherwise not open the picker.
 */
const SPACELESS_SCRIPT =
  /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}\p{Script=Thai}\p{Script=Lao}\p{Script=Khmer}\p{Script=Myanmar}\p{Script=Tibetan}\u3099\u309A\u30FC\uFF70\uFF9E\uFF9F]/u;

/**
 * True when an `@` typed immediately after `before` starts a token.
 *
 * `before` is the text preceding the `@`; only its last code point is read, and
 * an empty string means the `@` opens the text. Callers pass at most two code
 * units so an astral-plane code point arrives whole.
 */
export function isMentionBoundaryAfter(before: string): boolean {
  const codePoints = [...before];
  const previous = codePoints[codePoints.length - 1];
  if (previous === undefined) return true;
  return SPACELESS_SCRIPT.test(previous) || !WORD_CHARACTER.test(previous);
}
