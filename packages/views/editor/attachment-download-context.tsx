"use client";

// 本文件提供「附件下载上下文」：让 Tiptap 节点视图能按 URL 反查附件记录，
// 从而触发带鉴权的下载，而不是直接打开裸 URL。

import { createContext, use, useMemo, type ReactNode } from "react";
import type { Attachment } from "@multica/core/types";
import { matchAttachmentByURL } from "@multica/core/attachments/image-sequence";
import { openExternal } from "../platform";
import { useDownloadAttachment } from "./use-download-attachment";

interface ResolvedDownload {
  // 返回 markdown 里引用的 URL 对应的附件 id；如果是我们不管理的外部链接，
  // 返回 `undefined`。
  resolveAttachmentId: (url: string) => string | undefined;
  // 返回 markdown 里引用的 URL 对应的完整 Attachment 记录（content_type、
  // filename、download_url 等）。NodeView 的预览触发逻辑用它判断类型是否可
  // 预览，并喂给弹窗。
  resolveAttachment: (url: string) => Attachment | undefined;
  // 由 NodeView 的点击处理器调用。URL 能映射到已知附件时，通过 `getAttachment`
  // 重新签名后下载；URL 不是 Multica 管理的则回退到共享的外部链接处理器。
  openByUrl: (url: string) => void;
}

const AttachmentDownloadContext = createContext<ResolvedDownload | null>(null);

interface ProviderProps {
  attachments?: Attachment[];
  children: ReactNode;
}

/**
 * 给挂在 `ContentEditor` 里的 Tiptap NodeView 提供「点击时下载」处理器。没有
 * 这个 provider 时，消费者回退到用 `openExternal` 打开裸 URL——和这个 hook
 * 出现之前的行为一致。
 *
 * URL → 附件的匹配有按顺序的两个兜底。新评论（MUL-3130 之后）持久化的是稳定的
 * `/api/attachments/<id>/download` 形态；旧评论持久化的是上传时 `att.url` 里的
 * 任何内容，包括触发 MUL-3130 的那种短命 `/uploads/<key>?exp&sig` 形态。「从
 * URL 提取 id」处理新内容；「URL 完全相等」覆盖旧内容以及从没拿到新形态的
 * S3/CloudFront markdown。
 */
export function AttachmentDownloadProvider({ attachments, children }: ProviderProps) {
  const download = useDownloadAttachment();
  const value = useMemo<ResolvedDownload>(
    () => {
      // 与画廊序列构建器共用同一套匹配（MUL-5752），保证一个 markdown URL 不会
      // 在这里解析成 A 记录、在别处又解析成 B 记录。
      const lookup = (url: string): Attachment | undefined =>
        matchAttachmentByURL(url, attachments);
      return {
        resolveAttachmentId: (url) => lookup(url)?.id,
        resolveAttachment: lookup,
        openByUrl: (url) => {
          const att = lookup(url);
          if (att) {
            download(att.id);
            return;
          }
          if (url) openExternal(url);
        },
      };
    },
    [attachments, download],
  );
  return (
    <AttachmentDownloadContext.Provider value={value}>
      {children}
    </AttachmentDownloadContext.Provider>
  );
}

/**
 * 返回外层 `AttachmentDownloadProvider` 装好的「点击时下载」处理器；没有 provider
 * 时返回一个只把裸 URL 交给外部打开的回退实现。供 file-card 和图片 NodeView
 * 使用，让它们在还没接线好的编辑器界面里也能正常工作。
 */
export function useAttachmentDownloadResolver(): ResolvedDownload {
  const ctx = use(AttachmentDownloadContext);
  // hook 必须无条件调用：所以始终创建回退对象，但这里没必要 memo——因为每次
  // NodeView 渲染本来就会重新生成点击处理器的闭包。
  if (ctx) return ctx;
  return {
    resolveAttachmentId: () => undefined,
    resolveAttachment: () => undefined,
    openByUrl: (url) => {
      if (url) openExternal(url);
    },
  };
}
