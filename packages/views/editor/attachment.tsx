"use client";

/**
 * Attachment — 统一的附件渲染组件，所有附件展示场景共用这一个渲染入口。
 *
 * 接收一个「附件形状」的输入（可能是完整记录、仅 URL 引用，或正在上传的对象），
 * 然后按 PreviewKind（预览类型）分发到不同渲染器：
 *
 *   - image  → ImageAttachmentView（图片主体 + 悬停工具栏 + 通过共享的
 *              AttachmentPreviewModal 打开的灯箱）
 *   - html   → HtmlAttachmentPreview（内联 iframe + 悬停工具栏）
 *   - 其他   → AttachmentCard（图标 + 文件名 + 预览/下载按钮行）
 *
 * 调用方：
 *   - extensions/file-card.tsx 的 FileCardView（Tiptap 节点视图）
 *   - extensions/image-view.tsx 的 ImageView（Tiptap 节点视图）
 *   - rich-content/rich-content.tsx（markdown 图片与 fileCard 渲染器，
 *     让聊天、工单描述、评论都走同一个渲染器）
 *   - issues/components/comment-card.tsx 的 AttachmentList（独立兜底）
 *
 * 组件自己管理预览弹窗和下载分发逻辑——调用方只需传入 `attachment`，
 * 以及（编辑器场景下）可选的编辑器提示（selected、editable、onDelete）。
 */

import {
  Download,
  Link as LinkIcon,
  Maximize2,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { api } from "@multica/core/api";
import { useConfigStore } from "@multica/core/config";
import type { Attachment as AttachmentRecord } from "@multica/core/types";
import { useT } from "../i18n";
import { useAttachmentDownloadResolver } from "./attachment-download-context";
import { useAttachmentPreview } from "./attachment-preview-modal";
import { useImageSequencePreview } from "./image-sequence-context";
import {
  isObjectURL,
  useResignedInlineMediaURL,
} from "./hooks/use-inline-media-url";
import { useDownloadAttachment } from "./use-download-attachment";
import { AttachmentCard } from "./attachment-card";
import { HtmlAttachmentPreview } from "./html-attachment-preview";
import { getPreviewKind, type PreviewKind } from "./utils/preview";
import "./styles/attachment.css";

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * 附件输入：既可以是服务器返回的完整记录，也可以是正文里只带 URL 的轻量引用。
 * 组件内部会统一归一化成可渲染的状态。
 */
export type AttachmentInput =
  // 手里已经有服务器响应——完整记录。AttachmentList 以及任何遍历服务器返回的
  // attachments[] 数组的调用方都用这种形态。
  | { kind: "record"; attachment: AttachmentRecord }
  // Markdown / Tiptap 内联场景：只有 URL + 文件名。若外层有
  // AttachmentDownloadProvider，会解析回完整记录；否则退回仅 URL 模式
  // （媒体类型仍能从 URL 预览，文本类型则退回成一个下载按钮）。
  | {
      kind: "url";
      url: string;
      filename: string;
      contentType?: string;
      /** 编辑器里「上传中」的状态，此时渲染一个加载占位。 */
      uploading?: boolean;
      /**
       * 图片固有像素尺寸。会渲染成 `<img width height>`，让浏览器在图片解码前
       * 就预留好占位框——避免粘贴时因布局抖动把光标挤出视野。
       */
      width?: number;
      height?: number;
      /**
       * 调用方给出的「结构提示」：这个位置本质上就是图片 / 文件 / …。
       * 它绕过了 `getPreviewKind` 的自动探测（后者需要文件名或 content-type，
       * 两者都没有时会回退成文件卡片外观）。适用于那些「从上下文就知道类型，
       * 但只拿到一个 URL、alt/文件名又是空的」的调用方（markdown 的 `![]()`
       * 一定是图片；Tiptap 的图片 NodeView 也一定是图片）。
       */
      forceKind?: PreviewKind;
    };

/** 附件组件的 props。 */
export interface AttachmentProps {
  /** 要渲染的附件输入（记录 / URL 引用 / 上传中）。 */
  attachment: AttachmentInput;
  /** 编辑器提示——为 true 时，图片工具栏会显示删除（垃圾桶）按钮。 */
  editable?: boolean;
  /** 编辑器提示——给图片主体套上「选中」的高亮样式。 */
  selected?: boolean;
  /** 编辑器提示——接到 Tiptap 的 deleteNode() 上。 */
  onDelete?: () => void;
  className?: string;
}

interface Normalized {
  filename: string;
  contentType: string;
  url: string;
  attachmentId?: string;
  record?: AttachmentRecord;
  uploading: boolean;
  width?: number;
  height?: number;
}

function normalize(
  input: AttachmentInput,
  resolve: (url: string) => AttachmentRecord | undefined,
  cdnDomain: string,
  cdnSigned: boolean,
): Normalized {
  if (input.kind === "record") {
    return {
      filename: input.attachment.filename,
      contentType: input.attachment.content_type,
      url: absolutizeMediaURL(
        pickInlineMediaURL(input.attachment, input.attachment.url, cdnDomain, cdnSigned),
      ),
      attachmentId: input.attachment.id,
      record: input.attachment,
      uploading: false,
    };
  }
  const record = input.url ? resolve(input.url) : undefined;
  return {
    filename: input.filename || record?.filename || "",
    contentType: input.contentType || record?.content_type || "",
    // 当 markdown 里的 URL 能解析回附件记录时，换成该记录上「当下可直接加载」的
    // URL。持久化的 markdown URL（新内容是 `/api/attachments/<id>/download`，
    // 旧内容是原始存储 URL）作为稳定引用没问题，但不一定能在每个客户端上直接
    // 当作原生 <img>/<video> 资源加载——token 模式下客户端没法给裸的 /api/*
    // 请求带上 Authorization 头，而 CloudFront 签名过的 `download_url` 才是
    // 那种模式下唯一能用的媒体地址。`pickInlineMediaURL` 会优先挑自带凭据的
    // URL，没有时退回输入 URL，这样旧的 / 解析不出来的 markdown 仍走原路径。
    // 详见 MUL-3130 评审。
    //
    // 挑出自带凭据的 URL 之后，再做一次 absolutize（补全绝对地址）处理：
    // 当文档源并没有把 /api 或 /uploads 反向代理到 API 主机时（比如移动端
    // webview），站点相对路径 `/api/attachments/...` 或 `/uploads/...` 需要
    // 补上源才能加载。Web 端走同源代理，`apiBaseUrl=""`，这个辅助函数在那里
    // 等于空操作。详见 MUL-3192——quick-create 曾因不同源的客户端收到站点相对
    // 的图片 URL 而回退。
    url: absolutizeMediaURL(
      record ? pickInlineMediaURL(record, input.url, cdnDomain, cdnSigned) : input.url,
    ),
    attachmentId: record?.id,
    record,
    uploading: !!input.uploading,
    width: input.width,
    height: input.height,
  };
}

// absolutizeMediaURL 是针对旧 markdown 正文的兼容兜底：那些正文里持久化的是
// 站点相对路径 `/api/attachments/<id>/download` 或 `/uploads/<key>`。
//
// 当前（MUL-3192 之后）的写入路径会持久化一个由服务端 `buildMarkdownURL`
// 挑好的绝对 URL（见 server/internal/handler/file.go），所以新内容在每个客户端
// 都能原生加载。这个辅助函数只对 MUL-3192 之前写的内容有意义——那些正文里
// 还是旧的相对路径形态，把它们渲染在「文档源不是 API 主机」的界面（比如移动端
// webview）时，需要在渲染时把 API 基地址补进去。
//
// 在 Web 端 `api.getBaseUrl()` 是空的（Next.js 的 rewrite 在服务端把 /api/*
// 代理到 API 主机），所以这里等于空操作。
//
// http(s)://、blob:、data: 这类 URL 原样透传——它们本身就带了自己的源。
function absolutizeMediaURL(rawUrl: string): string {
  if (!rawUrl) return rawUrl;
  if (/^https?:\/\//i.test(rawUrl)) return rawUrl;
  if (/^blob:/i.test(rawUrl) || /^data:/i.test(rawUrl)) return rawUrl;
  if (!rawUrl.startsWith("/")) return rawUrl;
  // api 单例是一个 Proxy，在 `setApiInstance()` 执行前（启动顺序、SSR 场景）
  // 对任何属性访问都返回 `undefined`。用可选链可以避免抛异常——初始化之前的
  // 渲染就保持站点相对路径不变。
  const baseUrl = (api.getBaseUrl?.() ?? "").replace(/\/+$/, "");
  if (!baseUrl) return rawUrl;
  return `${baseUrl}${rawUrl}`;
}

// pickInlineMediaURL 返回「最有可能在原生 <img>/<video>/<iframe> 资源加载里
// 成功」的 URL——也就是不需要调用方客户端附带 Authorization 头就能加载的地址。
//
// 元数据响应里每个附件行带三个 URL 字段，各自的有效期 / 可访问性都不同：
//
//   - `record.download_url` — 本次响应里的「点击时」URL。在 CloudFront 签名
//                             模式下是签名重定向地址（在 TTL 有效期内可当作
//                             原生 img src 使用）；其它模式下是裸的 API 端点
//                             （`/api/attachments/<id>/download`），需要每次
//                             请求鉴权，且无法在非同源界面（比如 Desktop 的
//                             file://）上当作原生 img 加载。
//   - `record.markdown_url` — 服务端为持久化挑好的长期 URL（MUL-3192 /
//                             `buildMarkdownURL`）：存储公开可读时走公共 CDN
//                             直连；私有桶模式下是 `MULTICA_PUBLIC_URL +
//                             /api/attachments/<id>/download`。构造上就与服务
//                             端策略对齐，所以只要两者同时存在，它都优先于
//                             `record.url`。
//   - `record.url`          — 原始存储 URL。可能是私有的（S3 /
//                             CloudFront 签名、R2、MinIO）无法直接加载。仅
//                             用于兜底那些没返回 `markdown_url` 的旧响应。
//
// 选择顺序：
//
//  1. 已签名的 `download_url` —— 当 CloudFront 为「本次响应」签发了签名重定向
//     时就用它；因为签名 URL 在首屏优于 `markdown_url`（少一次经过 API 端点的
//     跳转），而渲染器并不持久化它，所以 TTL 不是问题。
//  2. 已知 CDN 的 `record.url` —— 当 `/api/config` 暴露的 CDN 主机与附件记录
//     相同时，浏览器能直接加载对象（公共 CDN，或 CloudFront cookie 模式）。
//     让它优先于 API 形态的 `markdown_url`，这样渲染出的 `<img src>` 和「复制
//     链接」暴露的是 CDN URL，而持久化的 markdown 仍保持稳定的附件端点。当服务
//     端报告 `cdn_signed` 时跳过——CloudFront 签名 URL 模式下同一域名服务的是
//     私有内容，裸的（未签名）存储 URL 必然 403（MUL-3254）。
//  3. 本地磁盘的 `record.url` —— 自托管 LocalStorage 且没配
//     LOCAL_UPLOAD_BASE_URL 时，存的是站点相对路径 `/uploads/...`。它是直接的
//     静态对象 URL，在跨源客户端里经 `absolutizeMediaURL` 补上 apiBaseUrl 后
//     即可加载。
//  4. `record.markdown_url` —— 长期、与服务端策略对齐的 URL。它永远不会指向
//     私有桶（MUL-3192 评审中的 must-fix 2），所以优先于裸的 `record.url`，
//     除非是上面那条明确的站点相对本地上传路径。
//  5. `record.url` —— 兜底那些没返回 `markdown_url` 的旧响应（早于 MUL-3192
//     的后端）。
//  6. 输入 URL —— 当完全没有记录时使用。
function pickInlineMediaURL(
  record: AttachmentRecord,
  fallback: string,
  cdnDomain: string,
  cdnSigned: boolean,
): string {
  const dl = record.download_url ?? "";
  if (
    /^https?:\/\//i.test(dl) &&
    /[?&](Signature|X-Amz-Signature|Key-Pair-Id|Expires|X-Amz-Expires)=/i.test(dl)
  ) {
    return dl;
  }
  if (!cdnSigned && storageURLMatchesCdnDomain(record.url, cdnDomain)) return record.url;
  if (isSiteRelativeLocalUploadURL(record.url)) return record.url;
  if (record.markdown_url) return record.markdown_url;
  if (record.url) return record.url;
  return fallback;
}

function isSiteRelativeLocalUploadURL(rawURL: string): boolean {
  if (!rawURL || !rawURL.startsWith("/")) return false;
  const path = rawURL.split(/[?#]/, 1)[0] ?? "";
  return path === "/uploads" || path.startsWith("/uploads/");
}

function storageURLMatchesCdnDomain(rawURL: string, cdnDomain: string): boolean {
  const expected = normalizeHost(cdnDomain);
  if (!rawURL || !expected) return false;
  try {
    const u = new URL(rawURL);
    if (u.protocol !== "http:" && u.protocol !== "https:") return false;
    if (normalizeHost(u.hostname) !== expected) return false;
    return !hasExpiringSignatureQuery(u.searchParams);
  } catch {
    return false;
  }
}

function normalizeHost(host: string): string {
  return host.trim().toLowerCase().replace(/\.$/, "");
}

function hasExpiringSignatureQuery(q: URLSearchParams): boolean {
  for (const key of [
    "Signature",
    "X-Amz-Signature",
    "Key-Pair-Id",
    "Expires",
    "X-Amz-Expires",
  ]) {
    if (q.has(key)) return true;
  }
  return false;
}

// ---------------------------------------------------------------------------
// Dispatcher
// ---------------------------------------------------------------------------

/**
 * 附件统一渲染组件：根据附件类型（图片 / HTML / 其它）分发到对应的渲染器，
 * 并统一管理预览弹窗与下载逻辑。
 *
 * @param props.attachment - 要渲染的附件输入（记录 / URL 引用 / 上传中）
 * @param props.editable - 是否处于可编辑的编辑器场景（控制删除按钮等）
 * @param props.selected - 图片是否处于选中态（高亮样式）
 * @param props.onDelete - 删除回调（接 Tiptap 的 deleteNode）
 */
export function Attachment({
  attachment,
  editable,
  selected,
  onDelete,
  className,
}: AttachmentProps) {
  const { resolveAttachment, openByUrl } = useAttachmentDownloadResolver();
  const cdnDomain = useConfigStore((s) => s.cdnDomain);
  const cdnSigned = useConfigStore((s) => s.cdnSigned);
  const download = useDownloadAttachment();
  const preview = useAttachmentPreview();
  const sequence = useImageSequencePreview();

  const state = normalize(attachment, resolveAttachment, cdnDomain, cdnSigned);
  const forceKind =
    attachment.kind === "url" ? attachment.forceKind : undefined;
  const kind =
    forceKind ??
    (state.filename || state.contentType
      ? getPreviewKind(state.contentType, state.filename)
      : null);
  // 挑出的 URL 可能仍是带鉴权的 API 端点（重新打开的草稿，其持久化记录没有
  // 签名 download_url）。对于无法原生加载该端点的客户端，把它升级成新签名的
  // URL；对于根本没有签名 URL 可给的环境，则升级成「用带鉴权的字节抓取」构造
  // 出的 object URL。只有图片分支会做原生资源加载，所以也只有它选择走字节
  // 抓取。
  const mediaUrl = useResignedInlineMediaURL(
    state.attachmentId,
    state.url,
    kind === "image",
  );
  // object URL 只在当前会话有效，所以凡是会把 URL 交给用户或别的界面的地方，
  // 都改用那个长期有效的 URL。
  const shareUrl = isObjectURL(mediaUrl) ? state.url : mediaUrl;

  // 这张图片在外层界面的图片序列里的身份标识：URL 能解析回记录时用附件 id，
  // 否则就用正文里原样写的 URL——和 `collectImageSequence` 建键时用的那一对
  // 保持一致。
  const sequenceKey =
    state.attachmentId ?? (attachment.kind === "url" ? attachment.url : "");

  const openPreview = () => {
    // 在工单 / 聊天里，图片会在外层界面共享查看器的真实位置上打开，方便读者
    // 翻看其余图片。序列不认识的场景（编辑器里正在上传的图片、没有 provider 的
    // 界面）则回退到下面的单图预览。
    if (kind === "image" && sequence.openAt(sequenceKey)) return;
    if (state.record) {
      preview.tryOpen({
        kind: "full",
        attachment: {
          ...state.record,
          download_url: mediaUrl || state.record.download_url,
        },
      });
      return;
    }
    if (mediaUrl) {
      preview.tryOpen({
        kind: "url",
        url: mediaUrl,
        filename: state.filename,
      });
    }
  };

  const handleDownload = () => {
    if (state.attachmentId) {
      download(state.attachmentId);
      return;
    }
    if (shareUrl) openByUrl(shareUrl);
  };

  if (kind === "image") {
    return (
      <>
        <ImageAttachmentView
          src={mediaUrl}
          linkUrl={shareUrl}
          alt={state.filename}
          uploading={state.uploading}
          width={state.width}
          height={state.height}
          editable={editable}
          selected={selected}
          onView={openPreview}
          onDownload={handleDownload}
          onDelete={onDelete}
          className={className}
        />
        {preview.modal}
      </>
    );
  }

  if (kind === "html" && state.attachmentId && !state.uploading) {
    return (
      <>
        <HtmlAttachmentPreview
          attachmentId={state.attachmentId}
          filename={state.filename}
          onPreview={openPreview}
          onDownload={handleDownload}
          onDelete={editable ? onDelete : undefined}
        />
        {preview.modal}
      </>
    );
  }

  return (
    <>
      <AttachmentCard
        filename={state.filename}
        contentType={state.contentType}
        attachmentId={state.attachmentId}
        href={shareUrl || undefined}
        uploading={state.uploading}
        onPreview={openPreview}
        onDownload={handleDownload}
        onDelete={editable ? onDelete : undefined}
      />
      {preview.modal}
    </>
  );
}

// ---------------------------------------------------------------------------
// ImageAttachmentView — 内联图片 + 悬停工具栏
// ---------------------------------------------------------------------------
//
// DOM 和样式是有意直接沿用原 extensions/image-view.tsx 的 <figure> 结构。共享
// 的视觉样式放在 styles/attachment.css 的
// `.image-figure / .image-content / .image-toolbar` 下，这样独立界面（聊天消息、
// AttachmentList）无需依赖别处导入编辑器样式表就能得到一致的视觉效果。

interface ImageAttachmentViewProps {
  src: string;
  /**
   * 「复制链接」交给用户的 URL。只有当 `src` 是会话内的 object URL（代理模式的
   * 字节抓取兜底）时它才会与 `src` 不同——因为把 `blob:` URL 粘到本渲染器以外
   * 的任何地方都解析不出来。
   */
  linkUrl: string;
  alt: string;
  uploading: boolean;
  width?: number;
  height?: number;
  editable?: boolean;
  selected?: boolean;
  onView: () => void;
  onDownload: () => void;
  onDelete?: () => void;
  className?: string;
}

function ImageAttachmentView({
  src,
  linkUrl,
  alt,
  uploading,
  width,
  height,
  editable,
  selected,
  onView,
  onDownload,
  onDelete,
  className,
}: ImageAttachmentViewProps) {
  const { t } = useT("editor");

  const handleCopyLink = async () => {
    if (await copyText(linkUrl)) {
      toast.success(t(($) => $.image.link_copied));
    } else {
      toast.error(t(($) => $.image.copy_link_failed));
    }
  };

  // 只有在非编辑器 / 非上传中的界面，点击图片主体才会打开预览——编辑器里则把
  // 点击交给 ProseMirror 做选中 / 光标定位，预览改走显式的「放大」按钮。CSS
  // 规则 `.image-figure[data-clickable="true"] { cursor: zoom-in }` 也是靠这个
  // 标志来决定是否显示放大光标。
  const clickable = !editable && !uploading;

  // DOM 对齐原来的 ReadonlyImage（只用 span 链，这样渲染在 markdown <p> 里
  // 仍是合法 HTML5）。在编辑器场景里，NodeViewWrapper 仍会在外层再包一个自己
  // 的 .image-node div——多一个 `image-node` class 无伤大雅。
  return (
    <span className="image-node">
      <span
        className={cn(
          "image-figure",
          selected && editable && "image-selected",
          className,
        )}
        data-clickable={clickable || undefined}
        contentEditable={false}
        onClick={clickable ? onView : undefined}
      >
        <img
          src={src || undefined}
          alt={alt}
          width={width}
          height={height}
          className={cn("image-content", uploading && "image-uploading")}
          draggable={false}
        />
        {!uploading && src && (
          <span
            className="image-toolbar"
            onMouseDown={(e) => e.stopPropagation()}
            onClick={(e) => e.stopPropagation()}
          >
            <button type="button" onClick={onView} title={t(($) => $.image.view)}>
              <Maximize2 className="size-3.5" />
            </button>
            <button type="button" onClick={onDownload} title={t(($) => $.image.download)}>
              <Download className="size-3.5" />
            </button>
            <button type="button" onClick={handleCopyLink} title={t(($) => $.image.copy_link)}>
              <LinkIcon className="size-3.5" />
            </button>
            {editable && onDelete && (
              <button type="button" onClick={onDelete} title={t(($) => $.image.delete)}>
                <Trash2 className="size-3.5" />
              </button>
            )}
          </span>
        )}
      </span>
    </span>
  );
}
