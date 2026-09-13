// 本文件实现文件上传的 React Hook：把文件传给 API，并把服务器返回的附件记录
// 适配成编辑器需要的「上传结果」（带两种用途不同的 URL）。

"use client";

import { useState, useCallback } from "react";
import type { ApiClient } from "../api/client";
import type { Attachment } from "../types";
import { attachmentDownloadPath } from "../types/attachment-url";
import { MAX_FILE_SIZE } from "../constants/upload";

// 携带完整的 Attachment，让需要预览元数据（`content_type`、`download_url`）的
// 编辑器能直接拿到。对外暴露两个 URL 字段，因为它们的生命周期不同：
//
//   `link`         —— 与 `att.url` 同值。在 LocalStorage 后端下是短命的
//                    （HMAC 签名的 `/uploads/<key>`），在 S3 / CloudFront 下是
//                    长命的 CDN URL。头像/Logo 等调用方会把它的值持久化到
//                    `avatar_url` 这类字段里；只拿得到 URL、手上没有记录对象的
//                    消费方（例如 Markdown 渲染器）也直接加载它。让它在语义上
//                    始终等于 `att.url`，可以保住 MUL-3130 之前对非 Markdown
//                    调用方的契约——头像上传不会被改道到需要工作区成员资格的
//                    下载接口。
//
//   `markdownLink` —— 编辑器写进 Markdown 正文的 URL。来源是服务器下发的
//                    `att.markdown_url`，`buildMarkdownURL` 会按部署策略挑选
//                    （公开 CDN 的持久 URL，或
//                    `<MULTICA_PUBLIC_URL>/api/attachments/<id>/download`）。
//                    契约是「绝对地址、无 TTL、任何客户端都能原生加载」——正是
//                    它修复了 Desktop / 移动端 webview 里相对路径
//                    `/api/attachments/<id>/download` 对着 `file://` 无法解析
//                    的回归（MUL-3192）。
//
//                    服务器没下发 `markdown_url` 时，依次回退两层：
//                      1. `attachmentDownloadPath(att.id)` —— 旧的站内相对
//                         形态。在 Web 上可用（Next 的 rewrite 把 /api/* 代理到
//                         API 主机），也是老评论持久化的形态；非 Web 客户端的
//                         渲染面通过 attachment.tsx 的旧兼容逻辑做绝对化。
//                      2. `att.url` —— 「无工作区头像」分支，此时根本没有附件
//                         行 id。
//
// MUL-3130 把这两种语义塞进同一个 `link` 字段，引入了持久化图片的回归；
// MUL-3192 跟进把「持久 URL 怎么选」从客户端移到服务端，让持久化出来的形态
// 对当前部署总是正确，客户端无需知道自己在 web / desktop / mobile 哪个环境。
export type UploadResult = Attachment & {
  link: string;
  markdownLink: string;
};

/** 上传发生的上下文：可选地关联到某个 issue / 评论 / 聊天会话。 */
export interface UploadContext {
  issueId?: string;
  commentId?: string;
  chatSessionId?: string;
}

// pickMarkdownLink 选择编辑器会写进 Markdown 的那个 URL。
//
// 顺序：
//   1. `att.markdown_url` —— 服务端下发的持久 URL。这是 MUL-3192 引入的现代
//      契约；服务端（`buildMarkdownURL`）按部署情况决定发公开 CDN URL 还是
//      固定到 `MULTICA_PUBLIC_URL` 的绝对 API 地址。
//   2. `attachmentDownloadPath(att.id)` —— 站内相对路径的旧形态，为兼容早于
//      MUL-3192 的老后端而保留。Web 的 Next rewrite 能让它加载；桌面/移动端
//      走 attachment.tsx 的旧绝对化兜底。
//   3. `att.url` —— 没有附件行 id（UploadFile 的「无工作区头像」分支）时。
//      Markdown 调用方回退到存储后端给这个上传产出的 URL；是否持久化由调用方
//      负责。
function pickMarkdownLink(att: Attachment): string {
  if (att.markdown_url) return att.markdown_url;
  if (att.id) return attachmentDownloadPath(att.id);
  return att.url;
}

// 把一份完成的上传服务器记录 `Attachment` 适配成编辑器的 `UploadResult`
// （编辑器会写进 Markdown / 持久化的两个 URL 字段）。导出它，是为了让
// 「协调上传」路径（MUL-5181，请求走模块级的上传协调器而不是这个 hook）也能
// 给编辑器完全相同的形状做内联预览替换。
/**
 * 把服务器返回的附件记录适配成编辑器的上传结果。
 *
 * @param att - 服务端完成的附件记录
 * @returns 带 link 与 markdownLink 的上传结果
 */
export function toUploadResult(att: Attachment): UploadResult {
  return { ...att, link: att.url, markdownLink: pickMarkdownLink(att) };
}

/**
 * 文件上传 Hook：对外暴露 `upload`（静默失败）、`uploadWithToast`（失败回调）
 * 与 `uploading`（是否有上传在途）。
 *
 * @param api - API 客户端
 * @param onError - 失败回调；`uploadWithToast` 吞掉拒绝后通过它上报。
 *   传了它，调用方才能在失败 toast 里点名是哪个文件。`uploadWithToast`
 *   触发它之后会把 rejection 吞掉，所以什么都不传的调用方什么都收不到——
 *   但上传占位仍然会消失，单独看就像「我的文件悄悄不见了」（MUL-4808）。
 * @returns 上传方法与在途状态
 */
export function useFileUpload(
  api: ApiClient,
  // 出错时连同失败的 `file` 一起回调，让宿主能在失败 toast 里点名具体文件。
  // `uploadWithToast` 触发本回调后会吞掉 rejection，所以什么都不传的宿主就
  // 什么都报不了——但上传占位仍会消失，单独看就像文件被静默吞掉（MUL-4808）。
  onError?: (error: Error, file: File) => void,
) {
  // 在途计数，而不是单个布尔。调用方会并发发起多个上传（一次拖拽 N 个文件、
  // 粘贴多张图片），若用布尔，第一个上传的 finally 跑完它就会翻回 false——
  // 哪怕还有 N-1 个在途。消费 `uploading` 的界面（快捷创建的提交闸门、编辑器的
  // "Uploading…" 按钮文案）会因此在上传还没结束时放行提交，导致 `stripBlobUrls`
  // 把仍在途的图片从 Markdown 里抹掉、它们的附件 id 永远绑定不上（MUL-3339）。
  //
  // 对外暴露的 `uploading: boolean` 保持既有调用点契约（各处都是
  // `{ uploading } = useFileUpload(api)`），只是内部跟踪形态改成计数。
  const [inFlight, setInFlight] = useState(0);
  const uploading = inFlight > 0;

  const upload = useCallback(
    async (file: File, ctx?: UploadContext): Promise<UploadResult | null> => {
      if (file.size > MAX_FILE_SIZE) {
        throw new Error("File exceeds 100 MB limit");
      }

      setInFlight((n) => n + 1);
      try {
        const att: Attachment = await api.uploadFile(file, {
          issueId: ctx?.issueId,
          commentId: ctx?.commentId,
          chatSessionId: ctx?.chatSessionId,
        });
        return toUploadResult(att);
      } finally {
        setInFlight((n) => n - 1);
      }
    },
    [api],
  );

  const uploadWithToast = useCallback(
    async (file: File, ctx?: UploadContext): Promise<UploadResult | null> => {
      try {
        return await upload(file, ctx);
      } catch (err) {
        onError?.(err instanceof Error ? err : new Error("Upload failed"), file);
        return null;
      }
    },
    [upload, onError],
  );

  return { upload, uploadWithToast, uploading };
}
