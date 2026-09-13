"use client";

/**
 * attachments 模块：全页 HTML 附件查看器。
 *
 * 是 HtmlAttachmentPreview 工具栏里「浏览器标签页打开」动作的目的地。内联预览
 * （HtmlAttachmentPreview）在同一内容放进 480px 卡片并带悬停工具栏；这里是
 * 同一内容满出血展示，让用户能按全尺寸调整 / 与文档交互。
 *
 * 与内联预览相同的安全姿态：iframe sandbox 只有 "allow-scripts"——没有
 * allow-same-origin、没有 allow-top-navigation。iframe 运行在 opaque origin，
 * 无法触碰 cookie、localStorage、父页面或顶层导航。
 *
 * 路由按工作区限定（`/{slug}/attachments/{id}/preview`）以隔离租户；
 * `/api/attachments/{id}/content` 代理本身已做鉴权，slug 只是 URL 约定。
 */

import { useEffect } from "react";
import { useT } from "../i18n";
import { useAttachmentHtmlText } from "../editor/hooks/use-attachment-html-text";
import { withFragmentNavShim } from "../editor/utils/iframe-fragment-nav";
import { hashString } from "../editor/utils/hash-string";

/** 附件预览页组件的属性。 */
interface AttachmentPreviewPageProps {
  attachmentId: string;
  /** 可选展示名。缺省时退回通用标签；仅用于文档标题——绝不回显进 iframe
   *  sandbox。 */
  filename?: string;
}

/** 全页附件查看器：加载 HTML 附件内容并以沙箱 iframe 满页展示。 */
export function AttachmentPreviewPage({
  attachmentId,
  filename,
}: AttachmentPreviewPageProps) {
  const { t } = useT("editor");
  const query = useAttachmentHtmlText(attachmentId);

  const text = query.data?.text;

  // 展示名变化时更新浏览器标签标题。
  useEffect(() => {
    if (filename) document.title = filename;
  }, [filename]);

  const isLoading = query.isLoading;
  const isError = !isLoading && (!!query.error || !text);

  return (
    <div className="flex h-full w-full flex-col bg-background">
      {isLoading ? (
        <div className="flex flex-1 items-center justify-center text-body text-muted-foreground">
          {t(($) => $.attachment.preview_loading)}
        </div>
      ) : isError ? (
        <div
          className="flex flex-1 items-center justify-center px-4 text-body text-muted-foreground"
          data-testid="attachment-preview-page-error"
        >
          {t(($) => $.attachment.preview_failed)}
        </div>
      ) : (
        <iframe
          key={hashString(text as string)}
          srcDoc={withFragmentNavShim(text as string)}
          sandbox="allow-scripts"
          title={filename ?? "HTML attachment"}
          className="flex-1 w-full border-0 bg-background"
        />
      )}
    </div>
  );
}
