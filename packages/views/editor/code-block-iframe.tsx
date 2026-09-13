"use client";

/**
 * 共享的 HTML 预览 iframe。
 *
 * 使用者：
 *   - AttachmentCard 里的 InlineHtmlIframe（HTML 附件的行内预览）
 *   - CodeBlockView 的围栏 ```html 块（可编辑 Tiptap NodeView）
 *   - HtmlBlockPreview 的围栏 ```html 块（ReadonlyContent）
 *   - AttachmentPreviewModal 的全屏 HTML 类型
 *
 * 沙箱语义：
 *   sandbox="allow-scripts"（**不是** "allow-same-origin"）
 *   → iframe 运行在不透明源里：脚本能执行（图表 JS 能跑），但 cookie /
 *     localStorage / 访问父页面 / 顶层跳转 / 弹窗 / 表单仍被拦截。这是标准的
 *     「预览不可信 HTML」模型（HTML 规范 §iframe sandbox、MDN、Claude
 *     artifacts、v0.dev preview）。
 *
 * 服务端在 /api/attachments/{id}/content 的 `text/plain` + `nosniff` 防线
 * 保持不变——我们只把取到的文本正文喂给 iframe.srcDoc，绝不把 iframe.src
 * 指向代理 URL。
 */

import { cn } from "@multica/ui/lib/utils";

interface CodeBlockIframeProps {
  /** 作为 srcDoc 的文档源。空串渲染成空白框。 */
  html: string;
  /** iframe 的无障碍标题。 */
  title: string;
  className?: string;
  /** Tailwind 高度 token；默认 h-[480px]。 */
  heightClassName?: string;
}

/**
 * 把给定 HTML 渲染进受沙箱保护的 iframe 预览。
 */
export function CodeBlockIframe({
  html,
  title,
  className,
  heightClassName = "h-[480px]",
}: CodeBlockIframeProps) {
  return (
    <iframe
      // srcDoc 让正文留在父进程里，但借 sandbox 隔离到不透明源。关键：绝不
      // 把 `allow-scripts` 与 `allow-same-origin` 组合——那一对会按 HTML
      // 规范（sandbox 属性注释）直接击穿沙箱。
      srcDoc={html}
      sandbox="allow-scripts"
      title={title}
      className={cn(
        "w-full rounded-md border border-border bg-background",
        heightClassName,
        className,
      )}
    />
  );
}
