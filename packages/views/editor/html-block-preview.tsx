"use client";

/**
 * HtmlBlockPreview——围栏 ```html 代码块的只读渲染。
 *
 * 默认视图是「预览」（iframe），符合 V2 计划；用户可切到「源码」看高亮
 * 标记并复制。最大化在全屏 Dialog 里打开同一个 iframe。
 *
 * 由 ReadonlyContent 的 `code` 渲染器在 `lang === "html"` 时挂载。ReadonlyContent
 * 的 `pre` 渲染器按引用识别本组件，把它从默认 `<pre>` 包裹层里解包出来——
 * 与 MermaidDiagram 已有的两层技巧一致。
 *
 * 可编辑的 Tiptap NodeView **不用**它——那条路径必须保留
 * `<NodeViewContent as="code" />` 供用户继续输入。
 */

import { useState } from "react";
import {
  Check,
  Code as CodeIcon,
  Copy,
  Eye,
  Maximize2,
  RotateCw,
} from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { HtmlPreviewSurface } from "./html-preview-surface";
import { useT } from "../i18n";
import { CodeBlockStatic } from "./code-block-static";
import { HtmlPreviewBody } from "./html-preview-body";

const CODE_BLOCK_IFRAME_HEIGHT = "h-[480px]";

/**
 * CODE_BLOCK_IFRAME_HEIGHT 的像素孪生。预览 iframe 是固定高度，近视口惰性
 * 外壳（rich-content/lazy-rich-block.tsx）才能精确预留本组件将要占用的空间、
 * 零布局位移地挂载。两者要保持同步。
 */
export const HTML_BLOCK_PREVIEW_HEIGHT_PX = 480;

// 代码块头部显示的语言标签。不是可翻译字符串——它是语言标识符（匹配下面
// 的 `lang === "html"` token）。
const HTML_LANGUAGE_LABEL = "html";

interface HtmlBlockPreviewProps {
  html: string;
  className?: string;
}

/**
 * 只读 HTML 代码块：默认 iframe 预览，可切源码/复制/全屏。
 */
export function HtmlBlockPreview({ html, className }: HtmlBlockPreviewProps) {
  const { t } = useT("editor");
  const [view, setView] = useState<"preview" | "source">("preview");
  const [copied, setCopied] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [previewRevision, setPreviewRevision] = useState(0);

  const handleCopy = async () => {
    if (!html) return;
    if (await copyText(html)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };

  const toggleView = () =>
    setView((v) => (v === "preview" ? "source" : "preview"));

  return (
    <div className={cn("code-block-wrapper group/code relative my-3", className)}>
      <div
        className="absolute top-0 right-0 z-10 flex items-center gap-1.5 px-2 py-1.5 opacity-0 transition-opacity group-hover/code:opacity-100 focus-within:opacity-100"
      >
        <span className="text-caption text-muted-foreground select-none">{HTML_LANGUAGE_LABEL}</span>
        <button
          type="button"
          onClick={toggleView}
          className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
          title={
            view === "preview"
              ? t(($) => $.code_block.show_source)
              : t(($) => $.code_block.show_preview)
          }
          aria-label={
            view === "preview"
              ? t(($) => $.code_block.show_source)
              : t(($) => $.code_block.show_preview)
          }
        >
          {view === "preview" ? (
            <CodeIcon className="h-3.5 w-3.5" />
          ) : (
            <Eye className="h-3.5 w-3.5" />
          )}
        </button>
        {view === "preview" && <button type="button" title={t(($) => $.attachment.reload)}
          aria-label={t(($) => $.attachment.reload)} onClick={() => setPreviewRevision(value => value + 1)}
          className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground hover:bg-muted">
          <RotateCw className="h-3.5 w-3.5" />
        </button>}
        {view === "preview" && (
          <button
            type="button"
            onClick={() => setFullscreen(true)}
            className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
            title={t(($) => $.code_block.fullscreen)}
            aria-label={t(($) => $.code_block.fullscreen)}
          >
            <Maximize2 className="h-3.5 w-3.5" />
          </button>
        )}
        <button
          type="button"
          onClick={handleCopy}
          className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
          title={t(($) => $.code_block.copy_code)}
          aria-label={t(($) => $.code_block.copy_code)}
        >
          {copied ? (
            <Check className="h-3.5 w-3.5" />
          ) : (
            <Copy className="h-3.5 w-3.5" />
          )}
        </button>
      </div>
      <div hidden={view !== "preview" && !fullscreen}>
      <HtmlPreviewSurface open={fullscreen} onOpenChange={setFullscreen}
        inlineHeight={HTML_BLOCK_PREVIEW_HEIGHT_PX}
        title={t(($) => $.code_block.html_preview)}
        onReload={() => setPreviewRevision(value => value + 1)}>
        <HtmlPreviewBody key={previewRevision}
          source={{ kind: "inline", html }}
          title={t(($) => $.code_block.html_preview)}
          className={fullscreen ? "h-full w-full" : CODE_BLOCK_IFRAME_HEIGHT}
        />
      </HtmlPreviewSurface>
      </div>
      {view === "source" && <CodeBlockStatic language="xml" body={html} />}

    </div>
  );
}
