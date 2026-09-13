"use client";

/**
 * CodeBlockStatic——只读的 lowlight 高亮代码块。
 *
 * 使用者：
 *   - AttachmentPreviewModal 的 text 类兜底（从那里抽出来的）。
 *   - ReadonlyContent 里 HtmlBlockPreview 的「源码」切换。
 *
 * Tiptap 可编辑代码块 NodeView **不用**它：那条路径必须保留
 * `<NodeViewContent as="code" />` 供用户继续输入代码块内容。换成静态
 * lowlight 组件会冻结内容，并让 ProseMirror 状态与 DOM 失步。
 */

import { useMemo } from "react";
import { toHtml } from "hast-util-to-html";
import { cn } from "@multica/ui/lib/utils";
import { highlightCode } from "./syntax-highlight";
import "./styles/code.css";

interface CodeBlockStaticProps {
  language: string | undefined;
  body: string;
  className?: string;
}

/**
 * 把代码高亮成静态 HTML 块。
 */
export function CodeBlockStatic({ language, body, className }: CodeBlockStaticProps) {
  const html = useMemo(() => {
    const code = body.replace(/\n$/, "");
    try {
      const tree = highlightCode(code, language);
      return toHtml(tree) as string;
    } catch {
      // 别让一次意外的高亮失败搞坏预览。
      return escapeHtml(code);
    }
  }, [body, language]);

  return (
    <pre className={cn("rich-text-editor m-0 overflow-auto text-body", className)}>
      <code
        className={cn("hljs", language && `language-${language}`)}
        dangerouslySetInnerHTML={{ __html: html }}
      />
    </pre>
  );
}

/** 转义 HTML 特殊字符（高亮失败时的纯文本兜底）。 */
function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}
