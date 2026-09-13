/**
 * Markdown 复制扩展——让剪贴板的 text/plain 通道携带 Markdown 源码，
 * 而不是纯文本 textContent。
 *
 * 与 markdown-paste.ts 对称：
 *   粘贴:  text/plain  →  editor.markdown.parse  →  doc
 *   复制:  slice       →  editor.markdown.serialize  →  text/plain
 *
 * 为什么：ProseMirror 默认的 clipboardTextSerializer 会调 Slice.textBetween，
 * 把所有节点压平成内部文本。标题、列表、代码块、mention、文件卡片——全都
 * 丢掉各自的 Markdown 标记。粘贴进 VS Code、终端或聊天软件时就只剩裸文本。
 *
 * text/html 通道保持 ProseMirror 默认，这样贴回另一个 ProseMirror 编辑器
 * 时仍能借 data-pm-slice 精确还原节点结构。
 */
import { Extension } from "@tiptap/core";
import { Plugin, PluginKey } from "@tiptap/pm/state";
import type { Slice } from "@tiptap/pm/model";

// blob URL（blob:http://…）是进程级的；绝不能让它们流出页面。
const BLOB_IMAGE_RE = /!\[[^\]]*\]\(blob:[^)]*\)\n?/g;

/**
 * 创建「复制时输出 Markdown」的扩展。
 * @returns 一个 Tiptap 扩展，覆盖剪贴板的 text/plain 序列化器。
 */
export function createMarkdownCopyExtension() {
  return Extension.create({
    name: "markdownCopy",
    addProseMirrorPlugins() {
      const { editor } = this;

      const fallback = (slice: Slice) =>
        slice.content.textBetween(0, slice.content.size, "\n\n");

      return [
        new Plugin({
          key: new PluginKey("markdownCopy"),
          props: {
            clipboardTextSerializer(slice: Slice) {
              if (!editor.markdown) return fallback(slice);
              try {
                // 把 slice 内容包进一个临时 doc，让序列化器像处理真实文档
                // 一样遍历它。纯行内 slice 会自动包成 doc → paragraph；
                // 块级 slice 原样通过。
                const doc = editor.schema.topNodeType.create(
                  null,
                  slice.content,
                );
                const md = editor.markdown.serialize(doc.toJSON());
                return md.replace(BLOB_IMAGE_RE, "").replace(/\n+$/, "");
              } catch {
                // 特殊选区（如表格 cellSelection）包进 doc 节点可能过不了
                // schema 校验。回退兜底，复制永远不能坏。
                return fallback(slice);
              }
            },
          },
        }),
      ];
    },
  });
}
