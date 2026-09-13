"use client";

/**
 * HtmlAttachmentPreview——行内 HTML 附件渲染器。
 *
 * 视觉模型镜像图片渲染器：iframe 主体就是卡片，右上角悬浮工具条悬停浮现，
 * 提供预览（全屏弹窗）/ 新标签页打开 / 下载。没有文件卡片的 chrome
 * （图标 + 文件名行）。
 *
 * 没有「复制代码」按钮：这是**文件**，不是行内源码片段。行内 ```html```
 * 围栏块（HtmlBlockPreview）才是读/复制 HTML 源码的表面；附件的契约是
 * 查看 + 下载。
 *
 * 新标签页打开路由到浏览器页签里的 `/{slug}/attachments/{id}/preview`。
 *
 * 由统一 `<Attachment>` 分发器在附件为 HTML、且能解析出 `attachmentId`
 * （/content 代理按 id 键控）时挂载。其它类型由 `<Attachment>` 回退到共享的
 * AttachmentCard。
 *
 * 失败模式（413 / 415 / 传输错误）：我们不卸载 figure、也不回退到
 * AttachmentCard chrome——独立附件列表会过滤正文里已行内化的 URL，静默卸载
 * 会拿掉用户唯一的预览/下载入口。取而代之：主体塌缩成 80px 占位，工具条
 * 固定展开、所有动作保持可用。
 */

import { Download, ExternalLink, Maximize2, Trash2 } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { paths, useWorkspaceSlug } from "@multica/core/paths";
import { useT } from "../i18n";
import { useNavigation } from "../navigation";
import { useAttachmentHtmlText } from "./hooks/use-attachment-html-text";
import { HtmlPreviewBody } from "./html-preview-body";

const PREVIEW_HEIGHT = "h-[480px]";
const ERROR_PLACEHOLDER_HEIGHT = "h-20";

interface HtmlAttachmentPreviewProps {
  attachmentId: string;
  filename: string;
  onPreview: () => void;
  onDownload: () => void;
  onDelete?: () => void;
}

/**
 * 行内 HTML 附件：iframe 主体 + 悬浮工具条。
 */
export function HtmlAttachmentPreview({
  attachmentId,
  filename,
  onPreview,
  onDownload,
  onDelete,
}: HtmlAttachmentPreviewProps) {
  const { t } = useT("editor");
  // 订阅与主体相同的 React Query 缓存 key，让工具条在错误态也能固定展开。
  // 重新订阅是免费的——useQuery 去重保证不会多发请求。
  const query = useAttachmentHtmlText(attachmentId);
  const isError = !query.isLoading && (!!query.error || !query.data?.text);
  // 用 useWorkspaceSlug —— 不是 useWorkspacePaths。Paths 绑定变体在无 slug
  // 时会抛错；我们希望组件意外挂载在工作区路由之外时也能优雅渲染（只是
  // 藏起新标签页按钮）。
  const slug = useWorkspaceSlug();
  const { getShareableUrl } = useNavigation();

  // 只在工作区 slug 可解析时启用新标签页按钮——工作区上下文之外该路径
  // 没有意义。
  const canOpenInNewTab = !!slug && !!attachmentId;
  const handleOpenInNewTab = () => {
    if (!slug) return;
    const nameQuery = filename ? `?name=${encodeURIComponent(filename)}` : "";
    const path = `${paths.workspace(slug).attachmentPreview(attachmentId)}${nameQuery}`;
    const url = getShareableUrl(path);
    window.open(url, "_blank", "noopener,noreferrer");
  };

  return (
    <div
      className="group/html-preview relative my-1"
      onMouseDown={(e) => e.stopPropagation()}
    >
      <HtmlPreviewBody
        source={{ kind: "attachment", attachmentId }}
        title={filename}
        className={PREVIEW_HEIGHT}
        placeholderClassName={isError ? ERROR_PLACEHOLDER_HEIGHT : PREVIEW_HEIGHT}
        errorTestId="html-attachment-preview-error"
      />
      <div
        className={cn(
          "absolute right-2 top-2 flex items-center gap-0.5 rounded-md border border-border bg-background/95 p-0.5 shadow-sm transition-opacity",
          // 错误态固定展开工具条——内联渲染失败时，预览/下载是用户仅剩的
          // 逃逸通道。
          isError
            ? "opacity-100"
            : "opacity-0 group-hover/html-preview:opacity-100",
        )}
      >
        <button
          type="button"
          className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          title={t(($) => $.attachment.preview)}
          aria-label={t(($) => $.attachment.preview)}
          onMouseDown={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onPreview();
          }}
        >
          <Maximize2 className="h-3.5 w-3.5" />
        </button>
        {canOpenInNewTab && (
          <button
            type="button"
            className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            title={t(($) => $.attachment.open_in_new_tab)}
            aria-label={t(($) => $.attachment.open_in_new_tab)}
            onMouseDown={(e) => {
              e.preventDefault();
              e.stopPropagation();
              handleOpenInNewTab();
            }}
          >
            <ExternalLink className="h-3.5 w-3.5" />
          </button>
        )}
        <button
          type="button"
          className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          title={t(($) => $.image.download)}
          aria-label={t(($) => $.image.download)}
          onMouseDown={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onDownload();
          }}
        >
          <Download className="h-3.5 w-3.5" />
        </button>
        {onDelete && (
          <button
            type="button"
            className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive"
            title={t(($) => $.attachment.remove)}
            aria-label={t(($) => $.attachment.remove)}
            onMouseDown={(e) => {
              e.preventDefault();
              e.stopPropagation();
              onDelete();
            }}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
    </div>
  );
}
