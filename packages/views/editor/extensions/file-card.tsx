"use client";

/**
 * FileCard — 把上传的非图片文件渲染成样式化卡片（而非裸 markdown 链接）
 * 的 Tiptap 节点扩展。
 *
 * Markdown 序列化：`!file[filename](href)`——自定义语法，无歧义
 * （标准 `[name](url)` 无法与普通链接区分）。
 *
 * 载入管线：preprocess.ts 的 preprocessFileCards 会在 @tiptap/markdown 解析
 * 内容**之前**，把新版 `!file[name](url)` 语法与旧版 `[name](cdnUrl)` 行
 * 都转换成 HTML div。下面的 markdownTokenizer 作为绕过预处理、直接解析
 * markdown 时的兜底。
 */

import { Node, mergeAttributes } from "@tiptap/core";
import { ReactNodeViewRenderer, NodeViewWrapper } from "@tiptap/react";
import type { NodeViewProps } from "@tiptap/react";
import { FILE_CARD_URL_PATTERN } from "@multica/ui/markdown";
import { escapeMarkdownLabel } from "../utils/escape-markdown-label";
import { Attachment } from "../attachment";

// 反斜杠被排除出标签字符类，让 "\x" 只能由 \\. 消费——重叠的两支会按
// 2^n 种方式回溯（ReDoS，GitHub #4881）。
const FILE_CARD_MARKDOWN_RE = new RegExp(
  `^!file\\[((?:\\\\.|[^\\]\\\\])*)\\]\\((${FILE_CARD_URL_PATTERN.source})\\)`,
);


// ---------------------------------------------------------------------------
// React NodeView——薄封装层，所有渲染都在 <Attachment> 里
// ---------------------------------------------------------------------------

/**
 * FileCardView——fileCard 节点的 React 节点视图。
 * 仅把编辑器上下文（editable、deleteNode）透传给 <Attachment>。
 */
export function FileCardView({ node, editor, deleteNode }: NodeViewProps) {
  const href = (node.attrs.href as string) || "";
  const filename = (node.attrs.filename as string) || "";
  const uploading = node.attrs.uploading as boolean;
  const editable = editor?.isEditable ?? false;

  return (
    <NodeViewWrapper as="div" className="file-card-node" data-type="fileCard">
      <div contentEditable={false}>
        <Attachment
          attachment={{ kind: "url", url: href, filename, uploading }}
          editable={editable}
          onDelete={editable ? deleteNode : undefined}
        />
      </div>
    </NodeViewWrapper>
  );
}

// ---------------------------------------------------------------------------
// Tiptap Node 扩展
// ---------------------------------------------------------------------------

/**
 * FileCardExtension——把附件渲染成卡片行的 Tiptap 节点（markdown 形态为
 * `!file[name](url)`）。
 */
export const FileCardExtension = Node.create({
  name: "fileCard",
  group: "block",
  atom: true,

  addAttributes() {
    return {
      href: {
        default: "",
        rendered: false, // 不让 href 落到 DOM 上——避免触发链接行为
      },
      filename: {
        default: "",
        rendered: false,
      },
      fileSize: {
        default: 0,
        rendered: false,
      },
      uploading: {
        default: false,
        rendered: false,
      },
      uploadId: {
        default: null,
        rendered: false,
      },
    };
  },

  parseHTML() {
    return [
      {
        tag: 'div[data-type="fileCard"]',
        getAttrs: (el) => ({
          href: (el as HTMLElement).getAttribute("data-href"),
          filename: (el as HTMLElement).getAttribute("data-filename"),
        }),
      },
    ];
  },

  renderHTML({ node, HTMLAttributes }) {
    return [
      "div",
      mergeAttributes(HTMLAttributes, {
        "data-type": "fileCard",
        "data-href": node.attrs.href,
        "data-filename": node.attrs.filename,
      }),
    ];
  },

  // Markdown：自定义 !file[name](url) 语法，往返无歧义。
  // 标准 [name](url) 与普通链接不可区分——旧版 preprocessFileCards 里基于
  // 正则的 CDN 主机名匹配太脆弱。
  markdownTokenizer: {
    name: "fileCard",
    level: "block" as const,
    start(src: string) {
      return src.search(/^!file\[/m);
    },
    tokenize(src: string) {
      const match = src.match(FILE_CARD_MARKDOWN_RE);
      if (!match) return undefined;
      const filename = (match[1] ?? "").replace(/\\([[\]\\()])/g, "$1");
      return {
        type: "fileCard",
        raw: match[0],
        attributes: { filename, href: match[2] },
      };
    },
  },
  parseMarkdown: (token: any, helpers: any) => {
    return helpers.createNode("fileCard", token.attributes);
  },
  renderMarkdown: (node: any) => {
    const { href, filename, uploading } = node.attrs || {};
    // 进行中的占位符不是内容。文档本身就是草稿正文（getMarkdown →
    // setDraft），序列化它会把 `!file[x]()` 持久化——上面的 tokenizer 解析
    // 不回这一行，草稿里就留下活过上传、并随评论一起发出去的死亡字面文本。
    // 什么也不输出就是全部保证：卡片只在持有真实 href 的那一刻才成为内容，
    // 之前绝不。
    if (uploading === true || !href) return "";
    return `!file[${escapeMarkdownLabel(filename || "file")}](${href})`;
  },

  addNodeView() {
    return ReactNodeViewRenderer(FileCardView);
  },
});
