"use client";

/**
 * AttachmentCard — shared file-card row UI (icon + filename + Eye + Download).
 *
 * Subcomponent of the unified `<Attachment>` dispatcher (see attachment.tsx).
 * Rendered for every attachment kind that does not have a richer inline
 * renderer (image / html). Kind-aware routing lives in `<Attachment>` — keep
 * that decision out of this file so this stays a single-purpose row UI.
 */

import { Download, Eye, FileText, Loader2, Trash2 } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { getPreviewKind } from "./utils/preview";

export function AttachmentActionButton({
  label,
  onAction,
  overlay = false,
  destructive = false,
  children,
}: {
  label: string;
  onAction: () => void;
  overlay?: boolean;
  destructive?: boolean;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      className={cn(
        overlay
          ? "flex h-6 w-6 items-center justify-center rounded"
          : "shrink-0 rounded-md p-1",
        "text-muted-foreground transition-colors",
        destructive
          ? "hover:bg-destructive/10 hover:text-destructive"
          : overlay
            ? "hover:bg-muted hover:text-foreground"
            : "hover:bg-secondary hover:text-foreground",
      )}
      title={label}
      aria-label={label}
      onMouseDown={(event) => {
        event.preventDefault();
        event.stopPropagation();
        onAction();
      }}
    >
      {children}
    </button>
  );
}

interface AttachmentCardChromeProps {
  filename: string;
  uploading?: boolean;
  canPreview: boolean;
  canDownload: boolean;
  canDelete?: boolean;
  onPreview: () => void;
  onDownload: () => void;
  onDelete?: () => void;
}

function AttachmentCardChrome({
  filename,
  uploading,
  canPreview,
  canDownload,
  canDelete,
  onPreview,
  onDownload,
  onDelete,
}: AttachmentCardChromeProps) {
  const { t } = useT("editor");
  return (
    <div
      className="flex items-center gap-2 rounded-md border border-border bg-muted/50 px-2.5 py-1 transition-colors hover:bg-muted"
      onMouseDown={(e) => e.stopPropagation()}
    >
      {uploading ? (
        <Loader2 className="size-4 shrink-0 animate-spin text-muted-foreground" />
      ) : (
        <FileText className="size-4 shrink-0 text-muted-foreground" />
      )}
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm">
          {uploading
            ? t(($) => $.file_card.uploading, { filename })
            : filename}
        </p>
      </div>
      {!uploading && canPreview && (
        <AttachmentActionButton
          label={t(($) => $.attachment.preview)}
          onAction={onPreview}
        >
          <Eye className="size-3.5" />
        </AttachmentActionButton>
      )}
      {!uploading && canDownload && (
        <AttachmentActionButton
          label={t(($) => $.image.download)}
          onAction={onDownload}
        >
          <Download className="size-3.5" />
        </AttachmentActionButton>
      )}
      {!uploading && canDelete && onDelete && (
        <AttachmentActionButton
          label={t(($) => $.attachment.remove)}
          onAction={onDelete}
          destructive
        >
          <Trash2 className="size-3.5" />
        </AttachmentActionButton>
      )}
    </div>
  );
}

interface AttachmentCardProps {
  /** Filename used for icon label and previewable-kind detection. */
  filename: string;
  /** Content type used in addition to filename for previewable-kind detection. */
  contentType?: string;
  /**
   * Attachment id — required when the preview proxy is ID-keyed (text kinds
   * like markdown / html / text). Media kinds (pdf/video/audio) preview from
   * the URL alone.
   */
  attachmentId?: string;
  /** Download URL — used as a non-null sentinel for the download button. */
  href?: string;
  /** True while a synchronous upload is in flight (file-card NodeView only). */
  uploading?: boolean;
  /** Pressed when the Eye button is clicked. */
  onPreview: () => void;
  /** Pressed when the Download button is clicked. */
  onDownload: () => void;
  /** Optional remove button, used by editable comment/file-card surfaces. */
  onDelete?: () => void;
}

export function AttachmentCard({
  filename,
  contentType = "",
  attachmentId,
  href,
  uploading,
  onPreview,
  onDownload,
  onDelete,
}: AttachmentCardProps) {
  const kind = filename ? getPreviewKind(contentType, filename) : null;
  // Media kinds (pdf/video/audio) are previewable from a URL alone — the
  // modal renders them as <video>/<audio>/<iframe src=url>. Text kinds
  // (markdown/html/text) need the ID-keyed `/api/attachments/{id}/content`
  // proxy, so they only preview when we have an attachmentId — otherwise
  // the Eye button would call tryOpen, get rejected, and do nothing.
  const isUrlPreviewableKind =
    kind === "pdf" || kind === "video" || kind === "audio";
  const canPreview =
    !!href && kind !== null && (!!attachmentId || isUrlPreviewableKind);

  return (
    <div className="my-1">
      <AttachmentCardChrome
        filename={filename}
        uploading={uploading}
        canPreview={canPreview}
        canDownload={!!href}
        canDelete={!!onDelete}
        onPreview={onPreview}
        onDownload={onDownload}
        onDelete={onDelete}
      />
    </div>
  );
}
