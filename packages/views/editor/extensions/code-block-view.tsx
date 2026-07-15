"use client";

import { useEffect, useState } from "react";
import { NodeViewWrapper, NodeViewContent } from "@tiptap/react";
import type { NodeViewProps } from "@tiptap/react";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { MermaidDiagram } from "../mermaid-diagram";
import { CodeBlockIframe } from "../code-block-iframe";
import { CodeBlockToolbar } from "../code-block-toolbar";

// Coalesces fast keystrokes before re-rendering live previews.
// `mermaid.initialize()` mutates a process-global config, so back-to-back
// renders during typing can race a concurrent ReadonlyContent render
// (e.g. a comment card) and clobber its theme variables. 200ms keeps the
// "live preview" feel while making concurrent inits unlikely in practice.
// HTML preview reuses the same debounce: re-keying iframe.srcDoc on every
// keystroke causes the iframe to re-load and flicker.
const PREVIEW_DEBOUNCE_MS = 200;

const HTML_PREVIEW_HEIGHT = "h-[480px]";

function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(id);
  }, [value, delayMs]);
  return debounced;
}

function CodeBlockView({ node }: NodeViewProps) {
  const [copied, setCopied] = useState(false);
  // HTML blocks default to "preview"; the user can flip to "source" to
  // edit the markup directly. Note: the source `<pre>` MUST stay mounted
  // (just hidden) so ProseMirror keeps its NodeView bindings — unmounting
  // it would break editing.
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
    <NodeViewWrapper className="code-block-wrapper group/code relative my-2">
      {isMermaid && debouncedChart.trim() && (
        <div
          contentEditable={false}
          className="mermaid-diagram-preview mb-1"
        >
          <MermaidDiagram chart={debouncedChart} />
        </div>
      )}
      {isHtml && showHtmlPreview && (
        // CSS-hidden when toggled off so the `<pre>` below stays mounted —
        // unmounting either side would either lose ProseMirror bindings
        // (source) or thrash iframe.srcDoc (preview).
        <div contentEditable={false} className="mb-1">
          <CodeBlockIframe
            html={debouncedHtml}
            title="HTML 预览"
            heightClassName={HTML_PREVIEW_HEIGHT}
          />
        </div>
      )}
      <CodeBlockToolbar
        language={language}
        view={isHtml ? view : undefined}
        copied={copied}
        onToggleView={isHtml ? toggleView : undefined}
        onCopy={handleCopy}
        className="code-block-header"
      />
      {/* `<pre>` + NodeViewContent must remain mounted so the user can keep
          editing the code block contents. When the HTML preview is showing
          we just visually hide it — ProseMirror still tracks it. */}
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

export { CodeBlockView };
