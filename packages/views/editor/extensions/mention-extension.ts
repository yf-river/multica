import Mention from "@tiptap/extension-mention";
import { mergeAttributes } from "@tiptap/core";
import { ReactNodeViewRenderer } from "@tiptap/react";
import { MentionView } from "./mention-view";
import { escapeMarkdownLabel } from "../utils/escape-markdown-label";

/**
 * BaseMentionExtension——编辑器里的 @mention 节点。
 *
 * 基于 @tiptap/extension-mention，配 React 节点视图（MentionView）、
 * 自定义 HTML 渲染，以及 markdown 往返：
 *   - 序列化：`[@Label](mention://member/id)`（issue/project 不带 @ 前缀）
 *   - 解析：把 markdown 链接里的 mention:// 形态还原成 mention 节点
 */

const MENTION_LINK_MARKER = "](mention://";

/** 判断 index 处的字符是否被奇数个反斜杠转义。 */
function isEscaped(text: string, index: number): boolean {
  let slashCount = 0;
  for (let i = index - 1; i >= 0 && text[i] === "\\"; i--) {
    slashCount++;
  }
  return slashCount % 2 === 1;
}

/** 找第一个未被转义的 mention 链接标记 `](mention://`。 */
function findUnescapedMentionMarker(src: string, from = 0): number {
  let marker = src.indexOf(MENTION_LINK_MARKER, from);

  while (marker !== -1) {
    if (!isEscaped(src, marker)) return marker;
    marker = src.indexOf(MENTION_LINK_MARKER, marker + MENTION_LINK_MARKER.length);
  }

  return -1;
}

/** 从标记处向前找 mention 链接左括号 `[` 的位置。 */
function findMentionStart(src: string): number {
  let marker = findUnescapedMentionMarker(src);

  while (marker !== -1) {
    for (let i = marker - 1; i >= 0; i--) {
      const char = src[i];
      if (char === "\n" || char === "\r") break;
      if (char === "[" && !isEscaped(src, i)) return i;
    }

    marker = findUnescapedMentionMarker(src, marker + MENTION_LINK_MARKER.length);
  }

  return -1;
}

export const BaseMentionExtension = Mention.extend({
  addNodeView() {
    return ReactNodeViewRenderer(MentionView);
  },
  renderHTML({ node, HTMLAttributes }) {
    const type = node.attrs.type ?? "member";
    const prefix = type === "issue" || type === "project" ? "" : "@";
    return [
      "span",
      mergeAttributes(
        { "data-type": "mention" },
        this.options.HTMLAttributes,
        HTMLAttributes,
        {
          "data-mention-type": node.attrs.type ?? "member",
          "data-mention-id": node.attrs.id,
        },
      ),
      `${prefix}${node.attrs.label ?? node.attrs.id}`,
    ];
  },
  addAttributes() {
    return {
      ...this.parent?.(),
      type: {
        default: "member",
        parseHTML: (el: HTMLElement) =>
          el.getAttribute("data-mention-type") ?? "member",
        renderHTML: () => ({}),
      },
    };
  },
  markdownTokenizer: {
    name: "mention",
    level: "inline" as const,
    start(src: string) {
      // 先锚在 Multica 的 mention href 上。从每个 "[" 往前扫，遇到转义过的
      // 堆栈标记（如 \~\[...\]）会灾难性回溯。
      return findMentionStart(src);
    },
    tokenize(src: string) {
      // 标签接受转义字符（\\[ \\]）或任意非 ] 非 \ 的字符。把反斜杠排除
      // 出字符类让两个分支互斥——否则 "\x" 会按 2^n 种方式回溯
      // （ReDoS，GitHub #4881）——同时仍支持含方括号的名字如 "David\[TF\]"。
      const match = src.match(
        /^\[@?((?:\\.|[^\]\\])+)\]\(mention:\/\/(\w+)\/([^)]+)\)/,
      );
      if (!match) return undefined;
      // 反转 escapeMarkdownLabel：把 renderMarkdown 产出的 \[ \] \\ \( \)
      // 还原。必须与转义集完全镜像，否则含 "\" 的标签无法穿过线性
      // tokenizer 往返。
      const rawLabel = match[1]?.replace(/\\([[\]\\()])/g, "$1");
      return {
        type: "mention",
        raw: match[0],
        attributes: { label: rawLabel, type: match[2] ?? "member", id: match[3] },
      };
    },
  },
  parseMarkdown: (token: any, helpers: any) => {
    return helpers.createNode("mention", token.attributes);
  },
  renderMarkdown: (node: any) => {
    const { id, label, type = "member" } = node.attrs || {};
    const prefix = type === "issue" || type === "project" ? "" : "@";
    // 转义标签里的 [ ] \ ( )，避免弄坏 markdown 链接语法、也保证标签能
    // 穿过线性 tokenizer（它现在把 "\" 当转义引导符而非普通字符）。必须与
    // tokenize() 里的反转义保持同步。与 file-card/slash 共用
    // escapeMarkdownLabel。
    const safeLabel = escapeMarkdownLabel(label ?? id);
    return `[${prefix}${safeLabel}](mention://${type}/${id})`;
  },
});
