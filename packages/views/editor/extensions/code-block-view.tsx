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
import { Code as CodeIcon, Copy, Check, Eye } from "lucide-react";
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

const HTML_PREVIEW_HEIGHT = "h-[480px]";

function CodeBlockView({ node }: NodeViewProps) {
  const { t } = useT("editor");
  const [copied, setCopied] = useState(false);
  // HTML 块默认「预览」；用户可切到「源码」直接改标记。注意：源码 `<pre>`
  // 必须保持挂载（只是隐藏）让 ProseMirror 保住 NodeView 绑定——卸载它会
  // 破坏编辑。
  const [view, setView] = useState<"preview" | "source">("preview");
  const language = node.attrs.language || "";
  const isMermaid = language === "mermaid";
  const isHtml = language === "html";
  const chart = node.textContent;
  const debouncedChart = useDebouncedValue(
    isMermaid ? chart : "",
    PREVIEW_DEBOUNCE_MS,
  );
  const debouncedHtml = useDebouncedValue(
    isHtml ? chart : "",
    PREVIEW_DEBOUNCE_MS,
  );

  const handleCopy = async () => {
    const text = node.textContent;
    if (!text) return;
    if (await copyText(text)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };

  const showHtmlPreview = isHtml && view === "preview";
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
      {isHtml && showHtmlPreview && (
        // 切换走时用 CSS 隐藏，保证下面的 `<pre>` 一直挂载——任何一边被
        // 卸载都会要么丢 ProseMirror 绑定（源码侧），要么让 iframe.srcDoc
        // 反复重载（预览侧）。
        <div contentEditable={false} className="mb-1">
          <CodeBlockIframe
            html={debouncedHtml}
            title={t(($) => $.code_block.html_preview)}
            heightClassName={HTML_PREVIEW_HEIGHT}
          />
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
