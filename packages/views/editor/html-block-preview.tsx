"use client";

/**
 * HtmlBlockPreview — readonly rendering of fenced ```html code blocks.
 *
 * Default view is "preview" (iframe) per the V2 plan; user can flip to
 * "source" to see the highlighted markup and Copy it. Maximize opens the
 * same iframe in a full-screen Dialog.
 *
 * Mounted by ReadonlyContent's `code` renderer for `lang === "html"`. The
 * `pre` renderer in ReadonlyContent recognizes this component by reference
 * and unwraps it from the default `<pre>` envelope, matching the same
 * two-layer trick already used for MermaidDiagram.
 *
 * NOT used in the editable Tiptap NodeView — that path must keep
 * `<NodeViewContent as="code" />` so the user can continue typing.
 */

import { useState } from "react";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import {
  Dialog,
  DialogContent,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../i18n";
import { CodeBlockStatic } from "./code-block-static";
import { HtmlPreviewBody } from "./html-preview-body";
import { CodeBlockToolbar } from "./code-block-toolbar";

const CODE_BLOCK_IFRAME_HEIGHT = "h-[480px]";

// Label shown in the code-block header. Not a translatable string — it's a
// language identifier (matches the `lang === "html"` token below).
const HTML_LANGUAGE_LABEL = "html";

interface HtmlBlockPreviewProps {
  html: string;
  className?: string;
}

export function HtmlBlockPreview({ html, className }: HtmlBlockPreviewProps) {
  const { t } = useT("editor");
  const [view, setView] = useState<"preview" | "source">("preview");
  const [copied, setCopied] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);

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
    <div className={cn("code-block-wrapper group/code relative my-2", className)}>
      <CodeBlockToolbar
        language={HTML_LANGUAGE_LABEL}
        view={view}
        copied={copied}
        onToggleView={toggleView}
        onCopy={handleCopy}
        onFullscreen={() => setFullscreen(true)}
      />
      {view === "preview" ? (
        <HtmlPreviewBody
          source={{ kind: "inline", html }}
          title="HTML 预览"
          className={CODE_BLOCK_IFRAME_HEIGHT}
        />
      ) : (
        <CodeBlockStatic language="xml" body={html} />
      )}
      <Dialog open={fullscreen} onOpenChange={setFullscreen}>
        <DialogContent
          className="!max-w-6xl !h-[min(90vh,calc(100vh-2rem))] w-full p-0 gap-0 overflow-hidden"
          aria-label={t(($) => $.code_block.fullscreen)}
        >
          <HtmlPreviewBody
            source={{ kind: "inline", html }}
            title="HTML 预览"
            className="h-full w-full"
            iframeClassName="rounded-none border-0"
          />
        </DialogContent>
      </Dialog>
    </div>
  );
}
