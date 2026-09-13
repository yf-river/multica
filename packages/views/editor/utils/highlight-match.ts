/**
 * highlight-match——`==文字==` 高亮片段长相的唯一事实来源。
 * 编辑器的分词器（extensions/highlight.ts）与只读渲染降级
 * （utils/highlight-markdown.ts）都使用它，这样「存储 ↔ 编辑器 ↔ 只读」
 * 之间的边界规则永远不会漂移。
 *
 * 高亮 `==INNER==` 的规则：
 *   - 紧贴围栏内侧不能有空白（`==x==` 可以，`== x ==` 不可以）
 *   - INNER 非空（`====` 保持字面量）
 *   - 两侧围栏都不能落在代码/数学字面量片段内——行内代码/数学或围栏/
 *     `$$` 块里的 `==` 是字面量，不得开/关一段高亮（于是
 *     ``==a `b==c` d=="`` 高亮的是整体，内部的 `==` 仍是代码）
 *   - INNER 不得跨越空行/块边界（`==a\n\nb==` 是两个字面段落，
 *     与编辑器词法分析结果一致）
 */

/**
 * 字符区间 [start, end)，start 到 end 之间（含 start 不含 end）。
 */
export interface Range {
  start: number;
  end: number;
}

/**
 * 空行：一个换行、可选空格/Tab、再一个换行。
 * 兼容 LF 与 CRLF（Windows/粘贴）换行，保证无论内容怎么存，
 * 边界规则都成立。
 */
const BLANK_LINE_RE = /\r?\n[ \t]*\r?\n/;

/**
 * `==` 绝不能解释为高亮语法的字面量区间：围栏代码、行内代码，
 * 以及行内/展示数学。与 @multica/ui/markdown 的 linkify 里的代码区间
 * 扫描保持一致，让高亮与自动链接跳过相同的字面量上下文。较早
 * （块级）的区间优先，所以围栏块内的 `==`/反引号不会被再计为行内。
 */
export function findLiteralRanges(text: string): Range[] {
  const ranges: Range[] = [];
  const add = (re: RegExp) => {
    let m: RegExpExecArray | null;
    re.lastIndex = 0;
    while ((m = re.exec(text)) !== null) {
      const start = m.index;
      if (ranges.some((r) => start >= r.start && start < r.end)) continue;
      ranges.push({ start, end: start + m[0].length });
    }
  };
  add(/```[\s\S]*?```/g); // 围栏代码块
  add(/\$\$[\s\S]*?\$\$/g); // 展示数学块
  add(/(?<!\$)\$(?!\$)[^$\n]+\$(?!\$)/g); // 行内数学
  add(/(?<!`)`(?!`)[^`\n]+`(?!`)/g); // 行内代码
  return ranges;
}

function isInside(pos: number, ranges: Range[]): boolean {
  return ranges.some((r) => pos >= r.start && pos < r.end);
}

/**
 * 尝试匹配一个开头 `==` 落在 `i` 处的高亮片段。成功时返回结束偏移
 * （越过闭合 `==` 后的第一个位置）与内部文本；`i` 处不构成合法高亮则
 * 返回 null。
 *
 * 当调用方已经为整段文本算好了字面量区间（只读路径）时可以传入 `ranges`，
 * 否则内部会从 `text` 现场计算。
 */
export function matchHighlightAt(
  text: string,
  i: number,
  ranges?: Range[],
): { end: number; inner: string } | null {
  if (text[i] !== "=" || text[i + 1] !== "=") return null;
  const innerStart = i + 2;
  // 内部必须非空，且开头围栏后不能紧跟空白。
  if (innerStart >= text.length) return null;
  if (/\s/.test(text[innerStart]!)) return null;

  const r = ranges ?? findLiteralRanges(text);
  if (isInside(i, r)) return null; // 开头围栏是字面量（在代码/数学内）

  // INNER 不能跨空行：扫描范围止于第一个空行。
  const blankRel = text.slice(innerStart).search(BLANK_LINE_RE);
  const scanLimit = blankRel === -1 ? text.length : innerStart + blankRel;

  for (let j = innerStart + 1; j <= scanLimit && j + 1 < text.length; j++) {
    if (text[j] !== "=" || text[j + 1] !== "=") continue;
    if (isInside(j, r)) continue; // 闭合围栏是字面量——继续向后找
    if (/\s/.test(text[j - 1]!)) continue; // 围栏前不能紧跟空白
    return { end: j + 2, inner: text.slice(innerStart, j) };
  }
  return null;
}
