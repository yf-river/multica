// 本文件定义草稿里「可持久化的上传占位符 + 结果」类型与归一化逻辑（MUL-5181，
// L2）：从文件被选中那一刻起，上传就在持久化的草稿里有一条记录，上传的生命周期
// 不再依赖发起它的那个组件。

import type { Attachment } from "../types";

/**
 * 上传占位符的状态。
 */
export type UploadStatus = "uploading" | "uploaded" | "failed" | "interrupted";

/** 占位符与结果的公共字段。 */
interface DraftUploadBase {
  /** 客户端铸造的 id，在整个占位符生命周期内稳定。 */
  clientUploadId: string;
  filename: string;
  /** 字节大小，镜像一份，这样占位符无需持有 File 也能渲染。 */
  size: number;
  contentType?: string;
}

/**
 * 占位符，其字节尚未（或已不再）能对应到某个附件：
 *  - `uploading`：请求在途（由协调器持有）。
 *  - `failed`：请求出错；保留它，让用户看到这次失败。
 *  - `interrupted`：历史遗留，不再产生。MUL-5391 之前的构建会把一条「能扛过
 *    刷新」的 `uploading` 记录强转成这种；现在这种记录会被直接丢弃。仍然接受、
 *    渲染并允许消除它，让那些构建持久化下来的 blob 继续可用。
 */
export interface PendingDraftUpload extends DraftUploadBase {
  status: "uploading" | "failed" | "interrupted";
  /** 仅 `failed` 时存在：对外展示的错误信息。 */
  error?: string;
}

/** 一次已完成的上传，携带完整的服务端附件记录。 */
export interface UploadedDraftUpload extends DraftUploadBase {
  status: "uploaded";
  attachment: Attachment;
}

export type DraftUpload = PendingDraftUpload | UploadedDraftUpload;

/**
 * 判断一条记录是否已完成上传（会把类型收窄到 {@link UploadedDraftUpload}）。
 */
export function isUploaded(u: DraftUpload): u is UploadedDraftUpload {
  return u.status === "uploaded";
}

/** 按顺序取出所有已完成附件的记录，是提交时可绑定的那一组。 */
export function uploadedAttachments(uploads: readonly DraftUpload[]): Attachment[] {
  const out: Attachment[] = [];
  for (const u of uploads) {
    if (u.status === "uploaded") out.push(u.attachment);
  }
  return out;
}

/** 只要还有上传在途就返回 true——提交闸门读的就是它。 */
export function hasUploadingDraft(uploads: readonly DraftUpload[]): boolean {
  return uploads.some((u) => u.status === "uploading");
}

/**
 * 把一份已完成附件包成「已上传」占位符。
 *
 * @param attachment - 服务端完成的附件记录
 * @returns 对应的已上传占位符
 */
export function attachmentToDraftUpload(attachment: Attachment): UploadedDraftUpload {
  return {
    clientUploadId: attachment.id || attachment.url,
    status: "uploaded",
    filename: attachment.filename,
    size: attachment.size_bytes,
    contentType: attachment.content_type || undefined,
    // `download_url` / `attachment_download_url` 是针对当前 API 响应铸造的，
    // 可能是短命签名 URL；草稿上传要跨过对话框关闭和应用重启存活，所以这里把
    // 两者都剥掉。`url` / `markdown_url` 保留作为持久渲染/下载路径；内容编辑器
    // 的会话合并会用活的上传结果回填空的 download_url（下载流程会通过
    // getAttachment 重新拉取新鲜的 attachment_download_url）。
    attachment: { ...attachment, download_url: "", attachment_download_url: "" },
  };
}

/** 判断未知值长得是否像一个附件记录（有字符串 id / filename / url）。 */
function looksLikeAttachment(value: unknown): value is Attachment {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as { id?: unknown }).id === "string" &&
    typeof (value as { filename?: unknown }).filename === "string" &&
    typeof (value as { url?: unknown }).url === "string"
  );
}

/** 判断未知值是否已经是 DraftUpload 的形状（有字符串 clientUploadId / status）。 */
function isDraftUploadShape(value: unknown): value is DraftUpload {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as { clientUploadId?: unknown }).clientUploadId === "string" &&
    typeof (value as { status?: unknown }).status === "string"
  );
}

/**
 * 把原始持久化数组归一化成规范的 `DraftUpload[]`：
 *  - 已经是 `DraftUpload` 的条目保留，但仍在 `uploading` 的一律丢弃——刷新/
 *    重启无法续传那些字节，正文里也没有对应的占位节点（见下面注释）。
 *  - 裸的 `Attachment` 行（L2 之前的构建只存完成的附件）包成 `uploaded`。
 *  - 其他一律丢弃。
 *
 * 每次加载/水合以及每次 `set` 写入都会跑一遍，因此无论持久化的 blob 是哪份
 * 构建写的，内存形态始终是规范的。
 *
 * @param raw - 从持久化里读出的原始数组
 * @returns 归一化后的草稿上传列表
 */
export function normalizeStoredUploads(raw: unknown): DraftUpload[] {
  if (!Array.isArray(raw)) return [];
  const out: DraftUpload[] = [];
  for (const item of raw) {
    if (isDraftUploadShape(item)) {
      if (item.status === "uploading") {
        // 直接丢弃，而不是转成 `interrupted`。字节从未持久化，这次上传既无法
        // 续传也无法重试；文档里也没有它的节点，因为占位符从不被序列化。保留
        // 一条没有任何界面能处理的记录，只会让一份原本该空的草稿白白活满整个
        // TTL。正文里缺少该附件，正是提示用户「重新上传一次」的信号。
        continue;
      } else if (item.status === "uploaded") {
        // 仅当持久化的附件看起来还像个附件时才信任它。
        if (looksLikeAttachment((item as UploadedDraftUpload).attachment)) {
          out.push(item);
        }
      } else {
        out.push(item);
      }
    } else if (looksLikeAttachment(item)) {
      out.push(attachmentToDraftUpload(item));
    }
  }
  return out;
}
