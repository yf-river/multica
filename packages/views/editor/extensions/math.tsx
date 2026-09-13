"use client";

/**
 * KaTeX 数学公式节点扩展：`$...$`（行内）与 `$$...$$`（块级）。
 *
 * 两个节点都把公式表达式存进 attrs，用 KaTeX 渲染成 HTML 后以 React 节点
 * 视图展示（内容不可编辑），并各自实现了 markdown 往返：
 *   - 行内：`$expr$`，块级：`$$\n expr\n $$`
 *   - 解析只认 `$$...$$` 块（行内数学刻意不解析，见 InlineMathExtension
 *     里的说明），保留打字输入由显式块语法负责的约定
 */

import katex from "katex";
import { Node, mergeAttributes } from "@tiptap/core";
import { ReactNodeViewRenderer, NodeViewWrapper } from "@tiptap/react";
import type { NodeViewProps } from "@tiptap/react";

/** 把公式表达式渲染成 HTML 字符串（含 MathML，供无障碍阅读）。 */
function renderMath(expression: string, displayMode: boolean): string {
  return katex.renderToString(expression, {
    displayMode,
    output: "htmlAndMathml",
    strict: "warn",
    throwOnError: false,
  });
}

/** 行内公式的 React 节点视图：只读展示，双击/选中可查看原始表达式。 */
function InlineMathView({ node }: NodeViewProps) {
  const expression = String(node.attrs.expression ?? "");
  return (
    <NodeViewWrapper
      as="span"
      className="math-node inline"
      data-type="inline-math"
      data-expression={expression}
      contentEditable={false}
    >
      <span dangerouslySetInnerHTML={{ __html: renderMath(expression, false) }} />
    </NodeViewWrapper>
  );
}

/** 块级公式的 React 节点视图：整行展示，上下文中独立成块。 */
function BlockMathView({ node }: NodeViewProps) {
  const expression = String(node.attrs.expression ?? "");
  return (
    <NodeViewWrapper
      as="div"
      className="math-node block"
      data-type="block-math"
      data-expression={expression}
      contentEditable={false}
    >
      <div dangerouslySetInnerHTML={{ __html: renderMath(expression, true) }} />
    </NodeViewWrapper>
  );
}

/**
 * InlineMathExtension——行内数学 `$expr$`。
 *
 * 序列化时输出 `$...$`；解析端不提供单 $ 输入规则（见下方注释）。
 */
export const InlineMathExtension = Node.create({
  name: "inlineMath",
  group: "inline",
  inline: true,
  atom: true,
  selectable: true,

  addAttributes() {
    return {
      expression: {
        default: "",
        rendered: false,
      },
    };
  },

  parseHTML() {
    return [
      {
        tag: 'span[data-type="inline-math"]',
        getAttrs: (el) => ({
          expression: (el as HTMLElement).getAttribute("data-expression") ?? "",
        }),
      },
    ];
  },

  renderHTML({ node, HTMLAttributes }) {
    return [
      "span",
      mergeAttributes(HTMLAttributes, {
        "data-type": "inline-math",
        "data-expression": node.attrs.expression,
      }),
      node.attrs.expression,
    ];
  },

  // 单 $ 行内数学刻意不从 Markdown 解析、也不提供打字输入规则。`$100~$120`
  // 这类金额必须保持字面量；想写数学应显式使用 `$$...$$` 块。
  renderMarkdown: (node: any) => {
    const expression = String(node.attrs?.expression ?? "");
    return `$${expression}$`;
  },

  addNodeView() {
    return ReactNodeViewRenderer(InlineMathView);
  },
});

/**
 * BlockMathExtension——块级数学 `$$...$$`。
 *
 * markdown 解析专门识别 `$$...$$` 围栏块，序列化输出 `$$\n expr\n $$`。
 */
export const BlockMathExtension = Node.create({
  name: "blockMath",
  group: "block",
  atom: true,
  code: true,
  defining: true,
  isolating: true,
  selectable: true,

  addAttributes() {
    return {
      expression: {
        default: "",
        rendered: false,
      },
    };
  },

  parseHTML() {
    return [
      {
        tag: 'div[data-type="block-math"]',
        getAttrs: (el) => ({
          expression: (el as HTMLElement).getAttribute("data-expression") ?? "",
        }),
      },
    ];
  },

  renderHTML({ node, HTMLAttributes }) {
    return [
      "div",
      mergeAttributes(HTMLAttributes, {
        "data-type": "block-math",
        "data-expression": node.attrs.expression,
      }),
      node.attrs.expression,
    ];
  },

  markdownTokenizer: {
    name: "blockMath",
    level: "block" as const,
    start(src: string) {
      return src.search(/^\$\$/m);
    },
    tokenize(src: string) {
      if (!src.startsWith("$$")) return undefined;
      const match = src.match(/^\$\$[ \t]*\n?([\s\S]+?)\n?\$\$(?:\n|$)/);
      if (!match) return undefined;
      return {
        type: "blockMath",
        raw: match[0],
        attributes: { expression: match[1] ?? "" },
      };
    },
  },

  parseMarkdown: (token: any, helpers: any) => {
    return helpers.createNode("blockMath", token.attributes);
  },

  renderMarkdown: (node: any) => {
    const expression = String(node.attrs?.expression ?? "");
    return `$$\n${expression}\n$$`;
  },

  addNodeView() {
    return ReactNodeViewRenderer(BlockMathView);
  },
});
