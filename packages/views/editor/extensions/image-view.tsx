"use client";

/**
 * ImageView — image 节点的 Tiptap NodeView。
 *
 * 统一 `<Attachment>` 分发器的薄封装。所有渲染（figure、悬停工具栏、
 * 灯箱/预览）都在那边。NodeView 只转发 Tiptap 的编辑器上下文提示
 * （editable、selected、deleteNode）。
 */

import { NodeViewWrapper } from "@tiptap/react";
import type { NodeViewProps } from "@tiptap/react";
import { Attachment } from "../attachment";

function ImageView({ node, editor, selected, deleteNode }: NodeViewProps) {
  const src = (node.attrs.src as string) || "";
  const alt = (node.attrs.alt as string) || "";
  const uploading = node.attrs.uploading as boolean;
  const width = (node.attrs.width as number | null) ?? undefined;
  const height = (node.attrs.height as number | null) ?? undefined;

  // <Attachment> 自己会输出 .image-node 包裹层，所以这里的 NodeViewWrapper
  // 不带 class——避免出现两层 image-node。
  return (
    <NodeViewWrapper>
      <Attachment
        attachment={{
          kind: "url",
          url: src,
          filename: alt,
          uploading,
          // 固有尺寸让浏览器在解码前预留 <img> 占位框（避免跳动）。
          width,
          height,
          // Tiptap image 节点无论 alt 是什么，结构上都是图片。
          forceKind: "image",
        }}
        editable={editor.isEditable}
        selected={selected}
        onDelete={() => deleteNode()}
      />
    </NodeViewWrapper>
  );
}

/**
 * ImageView——导出为图片节点视图组件（供 extensions/index.ts 挂载）。
 */
export { ImageView };
