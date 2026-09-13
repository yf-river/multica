"use client";

/**
 * HtmlPreviewBody——「在沙箱 iframe 里渲染 HTML 文档」的单一事实来源，
 * 无论来源是内联字符串（```html 围栏代码块）还是附件 id（HTML 附件）。
 *
 * 两条路径过去在 html-block-preview.tsx、html-attachment-preview.tsx 与
 * attachment-preview-modal.tsx 之间重复了 iframe + sandbox + 片段导航补丁
 * 的逻辑。本组件统一负责：
 *
 *   - 经 CodeBlockIframe 提供 srcDoc + sandbox="allow-scripts"
 *   - 加 withFragmentNavShim()，让 iframe 内的锚点链接能滚动、而不是在沙箱
 *     的不透明源里静默失败
 *   - 附件源的加载中 / 类型化错误占位
 *
 * 调用方（工具栏包裹层）保留各自的 chrome——代码块有复制/源码切换，附件有
 * 下载/新标签页打开——只需把 HtmlPreviewBody 作为主体塞进去。
 */

import { cn } from "@multica/ui/lib/utils";
import {
  PreviewTooLargeError,
  PreviewUnsupportedError,
} from "@multica/core/api";
import { useT } from "../i18n";
import { CodeBlockIframe } from "./code-block-iframe";
import { withFragmentNavShim } from "./utils/iframe-fragment-nav";
import { useAttachmentHtmlText } from "./hooks/use-attachment-html-text";

/** HTML 来源：直接给字符串，或给附件 id（走正文代理接口取文本）。 */
export type HtmlSource =
  | { kind: "inline"; html: string }
  | { kind: "attachment"; attachmentId: string };

interface HtmlPreviewBodyProps {
  source: HtmlSource;
  /** iframe 的 title 属性（无障碍）。 */
  title: string;
  /** iframe 的 Tailwind 高度/尺寸类（如 "h-[480px]" 或 "h-full"）。 */
  className?: string;
  /** 覆盖 iframe 样式（边框/圆角）。Tailwind-merge 解决冲突，调用方可为
   *  全屏场景传 "rounded-none border-0"。 */
  iframeClassName?: string;
  /** 加载中/错误占位的尺寸。默认沿用 className。 */
  placeholderClassName?: string;
  /** 错误占位上的可选 data-testid——保留给 html-attachment-preview.test.tsx，
   *  它断言该 testid 存在。 */
  errorTestId?: string;
}

/**
 * 渲染 HTML 预览主体：行内源直接进沙箱 iframe；附件源先取正文文本。
 */
export function HtmlPreviewBody({
  source,
  title,
  className,
  iframeClassName,
  placeholderClassName,
  errorTestId,
}: HtmlPreviewBodyProps) {
  if (source.kind === "inline") {
    return (
      <CodeBlockIframe
        html={withFragmentNavShim(source.html)}
        title={title}
        heightClassName={className}
        className={iframeClassName}
      />
    );
  }
  return (
    <AttachmentBody
      attachmentId={source.attachmentId}
      title={title}
      className={className}
      iframeClassName={iframeClassName}
      placeholderClassName={placeholderClassName ?? className}
      errorTestId={errorTestId}
    />
  );
}

/** 附件主体的内部实现：拉取正文，处理加载/错误占位。 */
function AttachmentBody({
  attachmentId,
  title,
  className,
  iframeClassName,
  placeholderClassName,
  errorTestId,
}: {
  attachmentId: string;
  title: string;
  className?: string;
  iframeClassName?: string;
  placeholderClassName?: string;
  errorTestId?: string;
}) {
  const { t } = useT("editor");
  const query = useAttachmentHtmlText(attachmentId);

  if (query.isLoading) {
    return (
      <div
        className={cn(
          "flex items-center justify-center rounded-md border border-border bg-muted/30 text-caption text-muted-foreground",
          placeholderClassName,
        )}
      >
        {t(($) => $.attachment.preview_loading)}
      </div>
    );
  }

  if (query.error || !query.data) {
    const message =
      query.error instanceof PreviewTooLargeError
        ? t(($) => $.attachment.preview_too_large)
        : query.error instanceof PreviewUnsupportedError
        ? t(($) => $.attachment.preview_unsupported)
        : t(($) => $.attachment.preview_failed);
    return (
      <div
        className={cn(
          "flex items-center rounded-md border border-border bg-muted/30 px-3 text-caption text-muted-foreground",
          placeholderClassName,
        )}
        data-testid={errorTestId}
      >
        <span className="truncate">{message}</span>
      </div>
    );
  }

  return (
    <CodeBlockIframe
      html={withFragmentNavShim(query.data.text)}
      title={title}
      heightClassName={className}
      className={iframeClassName}
    />
  );
}
