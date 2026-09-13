"use client";

/**
 * 共享的「附件文本正文」React Query 拉取 Hook。
 *
 * 统一走 `/api/attachments/{id}/content` 代理接口抓附件正文。
 *
 * 原本这套 retry / staleTime / gcTime 策略只存在于 AttachmentPreviewModal
 * 内部的 TextBackedPreview；把它抽出来共用，是为了让弹窗与内联的
 * AttachmentCard（file-card 节点视图 / 只读 file-card / 独立 AttachmentList）
 * 命中同一个缓存 key——内联预览已经加载过正文后，再打开弹窗不会重复请求。
 */

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";

/**
 * 拉取指定附件的文本正文。
 * @param attachmentId 附件 id；传空时查询不启用，返回未加载状态。
 */
export function useAttachmentHtmlText(attachmentId: string | null | undefined) {
  return useQuery({
    queryKey: ["attachment-content", attachmentId ?? ""] as const,
    queryFn: () => api.getAttachmentTextContent(attachmentId as string),
    enabled: !!attachmentId,
    // 413 / 415 这类状态码重试多少次也不会变成 200；传输类错误与其靠后台
    // 静默重试干等，不如直接失败让用户重开，恢复路径更清晰。
    retry: false,
    staleTime: 5 * 60_000,
    gcTime: 30 * 60_000,
  });
}
