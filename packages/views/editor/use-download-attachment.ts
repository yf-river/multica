"use client";

import { useCallback } from "react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceSlug } from "@multica/core/paths";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { useT } from "../i18n";

function attachmentDownloadEndpoint(
  attachmentId: string,
  workspaceSlug: string,
): string {
  const params = new URLSearchParams({ workspace_slug: workspaceSlug });
  const path = `/api/attachments/${encodeURIComponent(attachmentId)}/download`;
  const endpoint = `${path}?${params.toString()}`;
  return resolvePublicFileUrl(endpoint) ?? endpoint;
}

// 解析服务端铸造的「强制附件」（下载按钮）URL，如果有的话。与 `download_url`
//（加载意图——媒体为预览而内联服务）不同，`attachment_download_url` 在每种
// 存储模式下都带 Content-Disposition: attachment，所以它下载而不是预览。
// resolvePublicFileUrl 让站内相对的能力 URL 保持同源，并把绝对的
// presign / CloudFront URL 原样放行。服务端老于该字段时为空。
function forcedAttachmentUrl(url: string | undefined): string | null {
  if (!url) return null;
  return resolvePublicFileUrl(url);
}

// 服务端铸造的能力下载 URL，如果有的话。
//
// proxy 模式下后端把一份带签名的单附件能力塞进 `download_url`（站内相对的
// `/api/attachments/{id}/signed-download?...` URL，#6092 加的）。它在查询串里
// 自带凭据，所以一次顶层 `<a download>` 导航能自行鉴权——不需要
// `Authorization` 头、不需要会话 cookie——这正是解决那个 401 的方式，否则
// 浏览器会把错误正文存成 `download.txt`。那个 401 是 token 模式的情形：
// 鉴权是 JS 里持有的 bearer token，裸导航到 cookie 门控的 slug 端点根本
// 不会带任何凭据。
//
// 该能力**不**强制 attachment disposition——它经 `proxyAttachmentDownload`
// → `storage.ContentDisposition` 流式输出，图片/视频/音频/PDF 会返回
// `inline`。真正让浏览器下载而非预览的，是请求保持同源、加上 `download`
// 属性。这是给「有 #6092 能力但还没有 `attachment_download_url`」的服务端
// 的 #6713 回退；上面优先的强制附件 URL 补上了 #6712 跟踪的跨源媒体缺口。
//
// 绝对 CloudFront / S3 `download_url` 刻意不在这里用：它们跨源且按内联调校，
// 对它们做 `<a download>` 会预览而不是下载。那些模式仍从统一 slug 端点进入，
// 早于 #6092 的服务端则什么能力都不返回。
function capabilityDownloadUrl(downloadUrl: string | undefined): string | null {
  if (!downloadUrl) return null;
  if (!downloadUrl.startsWith("/api/attachments/")) return null;
  if (!downloadUrl.includes("/signed-download")) return null;
  return resolvePublicFileUrl(downloadUrl);
}

/** 触发浏览器下载（用隐藏 a 标签 + 点击）。 */
function triggerBrowserDownload(url: string): void {
  const anchor = document.createElement("a");
  anchor.href = url;
  // 让点击留在当前浏览上下文。对同源 API 下载，这个提示让
  // Chromium/Safari 能采用 Content-Disposition 的文件名而不开空白页签。
  // 若端点随后 302 到 CloudFront/S3，服务端会给那次重定向签 attachment
  // disposition；浏览器原生跟随它，不会把文件缓冲进 JS 内存。
  anchor.download = "";
  anchor.rel = "noopener";
  anchor.style.display = "none";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
}

/**
 * 返回一个按附件 id 触发下载的回调。Web 路径直接走统一服务端端点，
 * 而不是开空白页签或在渲染进程内存里把文件物化成 Blob。
 */
export function useDownloadAttachment(): (attachmentId: string) => Promise<void> {
  const { t } = useT("editor");
  const workspaceSlug = useWorkspaceSlug();
  return useCallback(
    async (attachmentId: string) => {
      const failed = () => toast.error(t(($) => $.attachment.download_failed));

      try {
        // 预检元数据请求既是一次权限检查（API 失败仍会弹出既有 toast，
        // 而不是一次静默失败的导航），也是服务端把为本次响应铸造的下载
        // URL 交回来的地方。
        const fresh = await api.getAttachment(attachmentId);
        if (typeof document === "undefined") {
          failed();
          return;
        }
        // 服务端铸造了强制附件 URL 时优先用：它无需凭据、每种存储模式都带
        // Content-Disposition: attachment，所以裸 `<a download>` 导航既能
        // 鉴权又能下载——而不是内联预览媒体、或在 token 模式下 401。
        const forced = forcedAttachmentUrl(fresh.attachment_download_url);
        if (forced) {
          triggerBrowserDownload(forced);
          return;
        }
        // 没有强制附件字段的老服务端：退回 #6092 proxy 能力（#6713）。
        // 顶层 `<a download>` 导航不带 `Authorization` 头，token 模式下
        // cookie 门控的 slug 端点会 401；能力 URL 在查询串里自带凭据，
        // 所以能自行鉴权那次裸导航。
        const capability = capabilityDownloadUrl(fresh.download_url);
        if (capability) {
          triggerBrowserDownload(capability);
          return;
        }
        // 没有能力（CloudFront / S3 签名，或早于 #6092 的服务端）：退回
        // cookie 鉴权的统一端点，它在会话 cookie 有效的任何地方都能用。
        if (!workspaceSlug) {
          failed();
          return;
        }
        triggerBrowserDownload(
          attachmentDownloadEndpoint(attachmentId, workspaceSlug),
        );
      } catch {
        failed();
      }
    },
    [t, workspaceSlug],
  );
}
