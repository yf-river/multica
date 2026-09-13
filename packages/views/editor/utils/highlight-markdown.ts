/**
 * highlight-markdown——只读场景的 `==文字==` → `<mark>文字</mark>` 降级转换。
 *
 * 编辑器（Tiptap）通过 HighlightExtension 的 markdownTokenizer 原生解析
 * `==text==`。只读展示面用的是 react-markdown，它不认识 `==` 高亮语法，
 * 所以在这里把它降级成裸 `<mark>` 元素。readonly-content.tsx 已经跑了
 * `rehype-raw`（因此裸 `<mark>` 会变成真实元素），并在 sanitize schema 里
 * 放行了 `mark`。因为内部文本原样保留，高亮里的嵌套 Markdown（比如
 * `==**bold**==` 或行内 `` `code` ``）仍由 remark 解析——与编辑器行为一致。
 *
 * 匹配规则在 highlight-match.ts 里，并与编辑器分词器共用，所以
 * 「存储 ↔ 编辑器 ↔ 只读」边界永不漂移：围栏不得位于代码/数学内，
 * 高亮不得跨越空行/块边界。
 */

import { findLiteralRanges, matchHighlightAt } from "./highlight-match";

/**
 * 把 `==text==` 高亮语法降级成裸 `<mark>`，供 react-markdown 只读管线使用。
 * 文本里没有 `==` 时原样返回。
 */
export function highlightToHtml(markdown: string): string {
  if (!markdown.includes("==")) return markdown;
  const ranges = findLiteralRanges(markdown);

  let result = "";
  let cursor = 0;
  let i = markdown.indexOf("==");
  while (i !== -1) {
    const match = matchHighlightAt(markdown, i, ranges);
    if (match) {
      result += markdown.slice(cursor, i);
      result += `<mark>${match.inner}</mark>`;
      cursor = match.end;
      i = markdown.indexOf("==", match.end);
    } else {
      // 此处不是合法高亮——跳过这对 `==` 继续找。
      i = markdown.indexOf("==", i + 2);
    }
  }
  result += markdown.slice(cursor);
  return result;
}
