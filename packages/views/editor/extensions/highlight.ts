import Highlight from "@tiptap/extension-highlight";
import { matchHighlightAt } from "../utils/highlight-match";

/**
 * HighlightExtension——文本高亮 mark（`==text==` ⇄ <mark>）。
 *
 * 基于 @tiptap/extension-highlight 构建，后者已提供 `<mark>` 的
 * parseHTML/renderHTML、`==text==` 的输入/粘贴规则、
 * setHighlight/toggleHighlight/unsetHighlight 命令，以及 Mod-Shift-H 快捷键。
 * 在此之上补上 @tiptap/markdown 序列化，让高亮以 `==text==` 的形态在存储的
 * Markdown 里往返：
 *
 *   - renderMarkdown: highlight mark → `==…==`。@tiptap/markdown 渲染 mark 时
 *     用一个占位子节点调用 renderMarkdown，再把结果拆成开头/结尾围栏，
 *     因此把占位符裹进 `==` 就得到 `==` 开、`==` 收。
 *   - markdownTokenizer + parseMarkdown: 存储 Markdown 里的 `==text==` → highlight
 *     mark。inlineTokens 保留内部的行内格式（例如 `==**bold**==`）。
 *
 * 目前单色（黄色）——`multicolor` 保持关闭。未来的多色变体需要能携带
 * 颜色的语法（`==text==` 做不到），所以这里刻意不纳入范围（见 MUL-2934）。
 *
 * 边界规则放在 utils/highlight-match.ts，并与只读渲染器
 * （utils/highlight-markdown.ts）共享，保证编辑器与只读视图对「什么算高亮」
 * 永远不会产生分歧：
 *   - 紧贴围栏内侧不能有空白（`==x==` 高亮，`== x ==` 不）
 *   - 内容非空（`====` 保持字面文本）
 *   - 两侧围栏都不能落在代码/数学里（`` `code` `` / `$math$` 内的 `==`
 *     是字面量），所以 ``==a `b==c` d==`` 高亮的是整段而不是 `a `b`
 *   - 高亮不得跨越空行/块边界
 */

export const HighlightExtension = Highlight.extend({
  markdownTokenizer: {
    name: "highlight",
    level: "inline" as const,
    start(src: string) {
      return src.indexOf("==");
    },
    tokenize(src: string, _tokens: unknown, helpers: any) {
      const match = matchHighlightAt(src, 0);
      if (!match) return undefined;
      return {
        type: "highlight",
        raw: src.slice(0, match.end),
        tokens: helpers.inlineTokens(match.inner),
      };
    },
  },

  parseMarkdown: (token: any, helpers: any) =>
    helpers.applyMark("highlight", helpers.parseInline(token.tokens)),

  renderMarkdown: (_node: any, helpers: any) => `==${helpers.renderChildren()}==`,
}).configure({ multicolor: false });
