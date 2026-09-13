"use client";

/**
 * AttachmentCard — 共享的「文件卡片行」UI（图标 + 文件名 + 预览/下载按钮）。
 *
 * 它是统一 `<Attachment>` 分发组件（见 attachment.tsx）的子组件。凡是那些没有
 * 更丰富内联渲染器（图片 / HTML）的附件类型，都会渲染成它。按类型路由的逻辑
 * 放在 `<Attachment>` 里——别把那个决定带进本文件，好让它保持单一职责的行级 UI。
 */

import { Download, Eye, FileText, Loader2, Trash2 } from "lucide-react";
import { useT } from "../i18n";
import { getPreviewKind } from "./utils/preview";

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
        <p className="truncate text-body">
          {uploading
            ? t(($) => $.file_card.uploading, { filename })
            : filename}
        </p>
      </div>
      {!uploading && canPreview && (
        <button
          type="button"
          className="shrink-0 rounded-md p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
          title={t(($) => $.attachment.preview)}
          aria-label={t(($) => $.attachment.preview)}
          onMouseDown={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onPreview();
          }}
        >
          <Eye className="size-3.5" />
        </button>
      )}
      {!uploading && canDownload && (
        <button
          type="button"
          className="shrink-0 rounded-md p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
          title={t(($) => $.image.download)}
          aria-label={t(($) => $.image.download)}
          onMouseDown={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onDownload();
          }}
        >
          <Download className="size-3.5" />
        </button>
      )}
      {!uploading && canDelete && onDelete && (
        <button
          type="button"
          className="shrink-0 rounded-md p-1 text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive"
          title={t(($) => $.attachment.remove)}
          aria-label={t(($) => $.attachment.remove)}
          onMouseDown={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onDelete();
          }}
        >
          <Trash2 className="size-3.5" />
        </button>
      )}
    </div>
  );
}

/** 文件卡片行的 props。 */
export interface AttachmentCardProps {
  /** 文件名，用于图标标签以及「是否可预览」的类型探测。 */
  filename: string;
  /** 内容类型，和文件名一起用于「是否可预览」的类型探测。 */
  contentType?: string;
  /**
   * 附件 id——当预览代理是按 id 寻址时（markdown / html / text 等文本类型）
   * 必需。媒体类型（pdf/video/audio）仅凭 URL 就能预览。
   */
  attachmentId?: string;
  /** 下载 URL——作为下载按钮的「非空哨兵」使用。 */
  href?: string;
  /** 上传进行中时为 true（仅 file-card NodeView 场景）。 */
  uploading?: boolean;
  /** 点击「预览」（眼睛）按钮时触发。 */
  onPreview: () => void;
  /** 点击「下载」按钮时触发。 */
  onDownload: () => void;
  /** 可选的删除按钮，用于可编辑的评论 / file-card 界面。 */
  onDelete?: () => void;
}

/**
 * 文件卡片行组件：渲染图标 + 文件名 + 预览/下载/删除按钮。
 *
 * @param props.filename - 文件名
 * @param props.contentType - 内容类型（用于判断是否可预览）
 * @param props.attachmentId - 附件 id（文本类预览代理需要）
 * @param props.href - 下载 URL（同时决定下载按钮是否显示）
 * @param props.onPreview - 预览回调
 * @param props.onDownload - 下载回调
 */
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
  // 媒体类型（pdf/video/audio）仅凭 URL 就能预览——弹窗会把它们渲染成
  // <video>/<audio>/<iframe src=url>。文本类型（markdown/html/text）则需要走
  // 按 id 寻址的 `/api/attachments/{id}/content` 代理，所以只有拿到 attachmentId
  // 时才能预览——否则眼睛按钮会调用 tryOpen、被拒绝、然后什么都不做。
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
