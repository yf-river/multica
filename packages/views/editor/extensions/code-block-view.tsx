"use client";

/**
 * CodeBlockView——代码块的 React 节点视图。
 *
 * 负责三件事：普通代码的语法高亮（由低层 CSS/lowlight 处理）、Mermaid
 * 图的实时预览、HTML 代码块的「预览/源码」切换，外加复制与显示语言名的
 * 工具条。源码 `<pre>` 必须始终挂载（见下方注释）。
 */

import { useState } from "react";
import { NodeViewWrapper, NodeViewContent } from "@tiptap/react";
import type { NodeViewProps } from "@tiptap/react";
import { Code as CodeIcon, Copy, Check, Eye, Maximize2, RotateCw } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { HtmlPreviewSurface } from "../html-preview-surface";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { useDebouncedValue } from "../../common/use-debounced-value";
import { useT } from "../../i18n";
import { MermaidDiagram } from "../mermaid-diagram";
import { CodeBlockIframe } from "../code-block-iframe";

// 合并快速击键后再去重渲染实时预览。`mermaid.initialize()` 会改动进程级
// 全局配置，打字时连续渲染可能抢跑一个并发的 ReadonlyContent 渲染（如评论
// 卡片）并把它的主题变量弄乱。200ms 既保持「实时预览」手感，又让并发 init
// 在实践中几乎不可能发生。HTML 预览复用同一防抖：每次击键都重设
// iframe.srcDoc 会让 iframe 反复加载并闪烁。
const PREVIEW_DEBOUNCE_MS = 200;

const HTML_PREVIEW_HEIGHT_PX = 480;

function CodeBlockView({ node }: NodeViewProps) {
  const { t } = useT("editor");
  const [copied, setCopied] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [previewRevision, setPreviewRevision] = useState(0);
  // HTML blocks default to "preview"; the user can flip to "source" to
  // edit the markup directly. Note: the source `<pre>` MUST stay mounted
  // (just hidden) so ProseMirror keeps its NodeView bindings — unmounting
  // it would break editing.
  const [view, setView] = useState<"preview" | "source">("preview");
  const language = node.attrs.language || "";
  const isMermaid = language === "mermaid";
  const isHtml = language === "html";
  const showHtmlPreview = isHtml && view === "preview";
  const chart = node.textContent;
  const debouncedChart = useDebouncedValue(
    isMermaid ? chart : "",
    PREVIEW_DEBOUNCE_MS,
  );
  const debouncedHtml = useDebouncedValue(
    isHtml ? chart : "",
    PREVIEW_DEBOUNCE_MS,
    showHtmlPreview || fullscreen,
  );

  const handleCopy = async () => {
    const text = node.textContent;
    if (!text) return;
    if (await copyText(text)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };

  const toggleView = () =>
    setView((v) => (v === "preview" ? "source" : "preview"));

  return (
    <NodeViewWrapper className="code-block-wrapper group/code relative my-3">
      {isMermaid && debouncedChart.trim() && (
        <div
          contentEditable={false}
          className="mermaid-diagram-preview mb-1"
        >
          <MermaidDiagram chart={debouncedChart} />
        </div>
      )}
      {isHtml && (
        // CSS-hidden when toggled off so the `<pre>` below stays mounted —
        // unmounting either side would either lose ProseMirror bindings
        // (source) or thrash iframe.srcDoc (preview).
        <div
          contentEditable={false}
          className={showHtmlPreview ? "mb-1" : undefined}
          hidden={!showHtmlPreview && !fullscreen}
        >
          <HtmlPreviewSurface
            open={fullscreen}
            inlineHeight={showHtmlPreview ? HTML_PREVIEW_HEIGHT_PX : 0}
            onOpenChange={setFullscreen}
            onReload={() => setPreviewRevision(value => value + 1)}
            title={t(($) => $.code_block.html_preview)}
          >
            <CodeBlockIframe key={previewRevision}
              html={debouncedHtml}
              title={t(($) => $.code_block.html_preview)}
              heightClassName={fullscreen ? "h-full" : "h-[480px]"}
              className={fullscreen ? "rounded-none border-0" : undefined}
            />
          </HtmlPreviewSurface>
        </div>
      )}
      <div
        contentEditable={false}
        className="code-block-header absolute top-0 right-0 z-10 flex items-center gap-1.5 px-2 py-1.5 opacity-0 transition-opacity group-hover/code:opacity-100 focus-within:opacity-100"
      >
        {language && (
          <span className="text-caption text-muted-foreground select-none">
            {language}
          </span>
        )}
        {isHtml && <button type="button" disabled={!showHtmlPreview && !fullscreen}
          title={t(($) => $.attachment.reload)} aria-label={t(($) => $.attachment.reload)}
          onClick={() => setPreviewRevision(value => value + 1)}
          className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground hover:bg-muted disabled:opacity-50">
          <RotateCw className="h-3.5 w-3.5" />
        </button>}
        {isHtml && (
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
        )}
        {isHtml && (
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            className="text-muted-foreground"
            title={t(($) => $.code_block.fullscreen)}
            aria-label={t(($) => $.code_block.fullscreen)}
            onClick={() => setFullscreen(true)}
          >
            <Maximize2 className="h-3.5 w-3.5" />
          </Button>
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
      {/* `<pre>` + NodeViewContent 必须保持挂载，用户才能继续编辑代码块
          内容。显示 HTML 预览时只是视觉上隐藏它——ProseMirror 仍跟踪着它。 */}
      <pre
        spellCheck={false}
        className={cn(showHtmlPreview && "sr-only")}
        aria-hidden={showHtmlPreview ? "true" : undefined}
      >
        {/* @ts-expect-error -- NodeViewContent supports as="code" at runtime */}
        <NodeViewContent as="code" />
      </pre>
    </NodeViewWrapper>
  );
}

/**
 * CodeBlockView——代码块节点视图组件（供扩展挂载）。
 */
export { CodeBlockView };
