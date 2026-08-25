import { Check, Code as CodeIcon, Copy, Eye, Maximize2 } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";

type CodeBlockViewMode = "preview" | "source";

export function CodeBlockToolbar({
  language,
  view,
  copied,
  onToggleView,
  onCopy,
  onFullscreen,
  className,
}: {
  language?: string;
  view?: CodeBlockViewMode;
  copied: boolean;
  onToggleView?: () => void;
  onCopy: () => void;
  onFullscreen?: () => void;
  className?: string;
}) {
  const { t } = useT("editor");
  const toggleLabel = view === "preview"
    ? t(($) => $.code_block.show_source)
    : t(($) => $.code_block.show_preview);
  const buttonClass =
    "flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted hover:text-foreground";

  return (
    <div
      contentEditable={false}
      className={cn(
        "absolute right-0 top-0 z-10 flex items-center gap-1.5 px-2 py-1.5 opacity-0 transition-opacity group-hover/code:opacity-100",
        className,
      )}
    >
      {language && <span className="select-none text-xs text-muted-foreground">{language}</span>}
      {view && onToggleView && (
        <button type="button" onClick={onToggleView} className={buttonClass} title={toggleLabel} aria-label={toggleLabel}>
          {view === "preview" ? <CodeIcon className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
        </button>
      )}
      {view === "preview" && onFullscreen && (
        <button type="button" onClick={onFullscreen} className={buttonClass} title={t(($) => $.code_block.fullscreen)} aria-label={t(($) => $.code_block.fullscreen)}>
          <Maximize2 className="h-3.5 w-3.5" />
        </button>
      )}
      <button type="button" onClick={onCopy} className={buttonClass} title={t(($) => $.code_block.copy_code)} aria-label={t(($) => $.code_block.copy_code)}>
        {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
      </button>
    </div>
  );
}
