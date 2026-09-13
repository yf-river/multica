// 每条评论草稿的持久化（MUL-5181）：正文 + 同一输入会话里发起的上传。
// 可抵抗以下丢失场景：虚拟化卸载（TipTap 编辑器滚出 Virtuoso 视口后内存态丢失——
// 这正是本 store 存在的原因）、误关标签页/Cmd-W、页面刷新。
// 按"工作区隔离的存储 + 议题维度 key"双重分区，见下方 CommentDraftKey 说明。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";
import { registerDraftCleanup } from "../../drafts/cleanup-registry";
import {
  type DraftUpload,
  type PendingDraftUpload,
  attachmentToDraftUpload,
  normalizeStoredUploads,
  uploadedAttachments,
} from "../../drafts/draft-upload";
import type { Attachment } from "../../types";

/**
 * 每条评论草稿的持久化——能扛住以下场景：
 *  - 虚拟化卸载（这就是它存在的原因：TipTap 编辑器滚出 Virtuoso 视口后，
 *    其内存态随之丢失）
 *  - 误关标签页 / 不小心 Cmd-W
 *  - 刷新页面
 *
 * 一份草稿同时保存"进行中的正文"和"同一输入会话里开始的上传"（MUL-5181）。
 * 上传以前放在组件的 `useState` 里，关闭输入框或把它滚走就会丢掉正在上传和
 * 已上传完成的文件；把它们存到这里，恢复能力和正文一样。每个上传是一个带状态的
 * {@link DraftUpload}——进行中的占位（归上传协调器管）落定后变成完整附件；
 * 而加载时仍是 `uploading` 的占位会被丢弃，因为字节从未被持久化过。
 *
 * key 要带议题维度：createWorkspaceAwareStorage 只按工作区分区、不按议题分。
 * key 里没有 issueId 的话，相邻桌面标签页里打开两个带线程回复的议题会互相冲突。
 */

/** 评论草稿的缓存 key 形状：
 * - `new:${issueId}` —— 顶级评论输入框
 * - `reply:${issueId}:${rootCommentId}` —— 线程里的回复输入框
 * - `edit:${issueId}:${commentId}` —— 对已有评论的行内编辑 */
export type CommentDraftKey =
  | `new:${string}`
  | `reply:${string}:${string}`
  | `edit:${string}:${string}`;

/** 一份评论草稿的内容：正文 + 该输入会话的上传（占位 + 已完成）。 */
interface CommentDraft {
  content: string;
  /** 本输入会话的上传（占位 + 已完成）。 */
  attachments: DraftUpload[];
  updatedAt: number;
}

interface CommentDraftStore {
  drafts: Record<string, CommentDraft>;
  /** 取回该草稿的正文；无草稿返回 undefined。 */
  getDraft: (key: CommentDraftKey) => string | undefined;
  /** 只取已完成附件的行——可提交 / 预览的那一组。 */
  getAttachments: (key: CommentDraftKey) => Attachment[];
  /** 取该草稿的所有上传（含占位）——用于状态徽标展示。 */
  getUploads: (key: CommentDraftKey) => DraftUpload[];
  /** 设置草稿正文。 */
  setDraft: (key: CommentDraftKey, content: string) => void;
  /**
   * 往草稿正文末尾追加一段 markdown（上传写回，MUL-5181）：一个在其输入框
   * 卸载后才完成的上传没有"活的"编辑器可插入链接，settle 处理器就把它落到这里——
   * 草稿正文始终是"最终绑定什么"的唯一真相来源。
   */
  appendToDraftContent: (key: CommentDraftKey, markdown: string) => void;
  /** 用已完成的附件替换上传列表（旧版/兼容路径）。 */
  setAttachments: (key: CommentDraftKey, attachments: Attachment[]) => void;
  /** 在选中文件的瞬间记录一个占位（协调器持有）。 */
  addUpload: (key: CommentDraftKey, upload: DraftUpload) => void;
  /** 把一个占位换成它完成的附件；若占位已不在则什么都不做。 */
  settleUpload: (key: CommentDraftKey, clientUploadId: string, attachment: Attachment) => void;
  /** 把占位标记为失败；若占位已不在则什么都不做。 */
  failUpload: (key: CommentDraftKey, clientUploadId: string, error?: string) => void;
  /** 丢弃一个占位（例如被关闭的失败提示）。 */
  removeUpload: (key: CommentDraftKey, clientUploadId: string) => void;
  /** 清空某条草稿。 */
  clearDraft: (key: CommentDraftKey) => void;
}

// 超过 30 天的草稿在 store 初始化时被清除。没有 TTL 的话，这个 store 会无限
// 累积每个议题的每次编辑尝试，慢慢吃光 localStorage 配额。
const TTL_MS = 30 * 24 * 60 * 60 * 1000;

// 稳定的空引用，让"读取缺失/空草稿"每次返回同一个数组——穿过 Zustand 选择器
// 的读取不能分配新对象，否则每次碰 store 都会让输入框重渲染。
const EMPTY_UPLOADS: DraftUpload[] = [];
const EMPTY_ATTACHMENTS: Attachment[] = [];

// 派生"已完成附件列表"按 uploads 数组引用做记忆化。每次变更都替换 uploads 数组，
// 因此引用稳定意味着派生列表不变——这让 `getAttachments` 选择器在无关的 store
// 触碰之间保持引用稳定。
const derivedCache = new WeakMap<DraftUpload[], Attachment[]>();
function deriveUploaded(uploads: DraftUpload[]): Attachment[] {
  const cached = derivedCache.get(uploads);
  if (cached) return cached;
  const derived = uploadedAttachments(uploads);
  const result = derived.length === 0 ? EMPTY_ATTACHMENTS : derived;
  derivedCache.set(uploads, result);
  return result;
}

// 一份草稿"有可恢复的意图"当且仅当：有非空白正文，或至少有一个上传（任何状态）。
// 仅上传的草稿（正文被删掉、但文件还在等待/失败）是有意义的——修剪或丢弃它会
// 悄悄毁掉整个持久化机制想要保护的上传。
function isMeaningful(content: string, uploads: DraftUpload[]): boolean {
  return content.trim().length > 0 || uploads.length > 0;
}

// setDraft/setAttachments/上传变更共用的写入口：一条既无正文也无上传的记录没有
// 可恢复内容，直接删掉而非留一份空白记录（与旧版 onUpdate 的"正文为空就清掉"对齐）。
function writeDraft(
  drafts: Record<string, CommentDraft>,
  key: string,
  content: string,
  uploads: DraftUpload[],
): Record<string, CommentDraft> {
  if (!isMeaningful(content, uploads)) {
    if (!(key in drafts)) return drafts;
    const next = { ...drafts };
    delete next[key];
    return next;
  }
  // 完全相同的写入保持幂等：输入框的 visibilitychange/pagehide 冲刷会在每次切标签
  // 页时重写同样内容；"过期提交"守卫会比较条目身份——一份"新但相等"的条目会被
  // 误读成"请求期间用户又编辑了"，从而错误地让已提交的草稿存活。
  const existing = drafts[key];
  if (existing && existing.content === content && existing.attachments === uploads) {
    return drafts;
  }
  return { ...drafts, [key]: { content, attachments: uploads, updatedAt: Date.now() } };
}

/** 读出某 key 的上传列表；草稿不存在时返回共享的空数组。 */
function uploadsOf(drafts: Record<string, CommentDraft>, key: string): DraftUpload[] {
  return drafts[key]?.attachments ?? EMPTY_UPLOADS;
}

/** 清理过期的持久化草稿（超出 TTL 的丢弃）。 */
function pruneStaleDrafts(drafts: Record<string, CommentDraft>): Record<string, CommentDraft> {
  const cutoff = Date.now() - TTL_MS;
  const out: Record<string, CommentDraft> = {};
  for (const [k, v] of Object.entries(drafts)) {
    // 归一化每条持久化草稿：遗留的 `Attachment[]` 变成已上传占位；任何仍处于
    // `uploading` 的占位被丢弃（字节从未持久化，上传无法续传）。
    const uploads = normalizeStoredUploads(v.attachments);
    if (v.updatedAt >= cutoff && isMeaningful(v.content, uploads)) {
      out[k] = { ...v, attachments: uploads };
    }
  }
  return out;
}

/** 评论草稿的 Zustand store：按 key 读写正文与上传，过期草稿自动清理。 */
export const useCommentDraftStore = create<CommentDraftStore>()(
  persist(
    (set, get) => ({
      drafts: {},
      getDraft: (key) => get().drafts[key]?.content,
      getAttachments: (key) => deriveUploaded(uploadsOf(get().drafts, key)),
      getUploads: (key) => uploadsOf(get().drafts, key),
      setDraft: (key, content) =>
        set((s) => ({
          drafts: writeDraft(s.drafts, key, content, uploadsOf(s.drafts, key)),
        })),
      appendToDraftContent: (key, markdown) =>
        set((s) => {
          const existing = s.drafts[key]?.content ?? "";
          // 正文非空时先压缩行尾空白再补两行空行，避免 markdown 粘在一起。
          const next = existing.trim() ? `${existing.replace(/\s+$/, "")}\n\n${markdown}` : markdown;
          return {
            drafts: writeDraft(s.drafts, key, next, uploadsOf(s.drafts, key)),
          };
        }),
      setAttachments: (key, attachments) =>
        set((s) => ({
          drafts: writeDraft(
            s.drafts,
            key,
            s.drafts[key]?.content ?? "",
            attachments.map(attachmentToDraftUpload),
          ),
        })),
      addUpload: (key, upload) =>
        set((s) => {
          const current = uploadsOf(s.drafts, key);
          // 同一 clientUploadId 已存在则跳过（幂等）。
          if (current.some((u) => u.clientUploadId === upload.clientUploadId)) return s;
          return {
            drafts: writeDraft(s.drafts, key, s.drafts[key]?.content ?? "", [...current, upload]),
          };
        }),
      settleUpload: (key, clientUploadId, attachment) =>
        set((s) => {
          const current = uploadsOf(s.drafts, key);
          // 占位已被移除则什么都不做（幂等）。
          if (!current.some((u) => u.clientUploadId === clientUploadId)) return s;
          const next = current.map((u) =>
            u.clientUploadId === clientUploadId
              ? { ...attachmentToDraftUpload(attachment), clientUploadId }
              : u,
          );
          return {
            drafts: writeDraft(s.drafts, key, s.drafts[key]?.content ?? "", next),
          };
        }),
      failUpload: (key, clientUploadId, error) =>
        set((s) => {
          const current = uploadsOf(s.drafts, key);
          const target = current.find((u) => u.clientUploadId === clientUploadId);
          // 占位已被移除则什么都不做。
          if (!target) return s;
          const failed: PendingDraftUpload = {
            clientUploadId,
            status: "failed",
            filename: target.filename,
            size: target.size,
            contentType: target.contentType,
            error,
          };
          const next = current.map((u) => (u.clientUploadId === clientUploadId ? failed : u));
          return {
            drafts: writeDraft(s.drafts, key, s.drafts[key]?.content ?? "", next),
          };
        }),
      removeUpload: (key, clientUploadId) =>
        set((s) => {
          const current = uploadsOf(s.drafts, key);
          // 占位已被移除则什么都不做。
          if (!current.some((u) => u.clientUploadId === clientUploadId)) return s;
          const next = current.filter((u) => u.clientUploadId !== clientUploadId);
          return {
            drafts: writeDraft(s.drafts, key, s.drafts[key]?.content ?? "", next),
          };
        }),
      clearDraft: (key) =>
        set((s) => {
          if (!(key in s.drafts)) return s;
          const next = { ...s.drafts };
          delete next[key];
          return { drafts: next };
        }),
    }),
    {
      name: "multica_comment_drafts",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      onRehydrateStorage: () => (state) => {
        // 从存储里水合回来时，先清一遍超出 TTL 的过期草稿。
        if (state) {
          state.drafts = pruneStaleDrafts(state.drafts);
        }
      },
    },
  ),
);

// 切换工作区后重水合该工作区的评论草稿。
registerForWorkspaceRehydration(() => useCommentDraftStore.persist.rehydrate());

// 登出清理：把这个 store 的内存态清空（localStorage 清理由 cleanup-registry 处理）。
registerDraftCleanup({
  storageKey: "multica_comment_drafts",
  workspaceScoped: true,
  resetInMemory: () => useCommentDraftStore.setState({ drafts: {} }),
});
