import Mention from "@tiptap/extension-mention";
import { mergeAttributes } from "@tiptap/core";
import { ReactNodeViewRenderer } from "@tiptap/react";
import { SlashCommandView } from "./slash-command-view";
import { formatSlashCommandLabel } from "./slash-command-utils";
import { escapeMarkdownLabel } from "../utils/escape-markdown-label";

/**
 * SlashCommandExtension——编辑里的「斜杠命令」/ 技能引用节点。
 *
 * 复用 Mention 的扩展骨架：斜杠技能以 `/技能名` 的标记插入文档，序列化成
 * `[/技能名](slash://skill/<id>)`，编辑态由 SlashCommandView 呈现。
 */
export const SlashCommandExtension = Mention.extend({
  name: "slashCommand",

  addNodeView() {
    return ReactNodeViewRenderer(SlashCommandView);
  },

  renderHTML({ node, HTMLAttributes }) {
    return [
      "span",
      mergeAttributes(
        { "data-type": "slash-command" },
        this.options.HTMLAttributes,
        HTMLAttributes,
      ),
      `/${formatSlashCommandLabel(node.attrs.label)}`,
    ];
  },

  addAttributes() {
    return { ...this.parent?.() };
  },

  parseHTML() {
    return [{ tag: 'span[data-type="slash-command"]' }];
  },

  markdownTokenizer: {
    name: "slashCommand",
    level: "inline" as const,
    start(src: string) {
      // 反斜杠被排除出字符类，使 "\x" 只能由 \\. 消费——否则重叠的两支
      // 会按 2^n 种方式回溯（ReDoS，GitHub #4881）。
      return src.search(/\[\/(?:\\.|[^\]\\])+\]\(slash:\/\/skill\//);
    },
    tokenize(src: string) {
      const match = src.match(
        /^\[\/((?:\\.|[^\]\\])+)\]\(slash:\/\/skill\/([^)]+)\)/,
      );
      if (!match) return undefined;
      // 反转 escapeMarkdownLabel：把 \[ \] \\ \( \) 还原。必须与转义集
      // 完全镜像，否则含 "\" 的标签无法穿过线性 tokenizer 往返。
      const rawLabel = match[1]?.replace(/\\([[\]\\()])/g, "$1");
      return {
        type: "slashCommand",
        raw: match[0],
        attributes: { label: rawLabel, id: match[2] },
      };
    },
  },

  parseMarkdown: (token: any, helpers: any) => {
    return helpers.createNode("slashCommand", token.attributes);
  },

  renderMarkdown: (node: any) => {
    const { id, label } = node.attrs || {};
    // 转义 [ ] \ ( ) 让标签能穿过线性 tokenizer；必须与上面 tokenize()
    // 里的反转义保持同步。
    const safeLabel = escapeMarkdownLabel(formatSlashCommandLabel(label));
    return `[/${safeLabel}](slash://skill/${id})`;
  },
});
