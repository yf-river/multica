"use client";

/**
 * 评论输入区与"协调上传引擎"的绑定（MUL-5181, L2）。
 *
 * 所有上传机制——协调器所有权、代际守卫、已完成上传的链接写回草稿正文、
 * 合并后的提交闸门——都在 `editor/use-coordinated-uploads` 里。
 * 本文件只把引擎映射到评论草稿 store 上，按键为 {@link CommentDraftKey}。
 * 没有 key（一个没有持久化上下文的回复框）时引擎退回到随挂载消亡的
 * 组件本地上传，与 L2 之前的旧行为一致。
 */

import { useMemo, type RefObject } from "react";
import type { DraftUpload } from "@multica/core/drafts";
import type { UploadContext } from "@multica/core/hooks/use-file-upload";
import { useCommentDraftStore, type CommentDraftKey } from "@multica/core/issues/stores";
import type { UploadGate } from "../../editor/use-upload-gate";
import type { ContentEditorRef } from "../../editor/content-editor";
import {
  useCoordinatedUploads,
  type CoordinatedUploads,
  type UploadDraftBinding,
} from "../../editor/use-coordinated-uploads";

const EMPTY_UPLOADS: DraftUpload[] = [];

/** 评论上传结果集合（转发协调上传引擎的类型）。 */
export type CommentUploads = CoordinatedUploads;

/**
 * 把协调上传引擎绑到"以 draftKey 分区的评论草稿 store"上：
 * 无 draftKey 时上传退化为组件本地、随挂载结束即失效（兼容旧行为）。
 */
export function useCommentUploads(
  draftKey: CommentDraftKey | undefined,
  ctx: UploadContext,
  editorGate: UploadGate,
  editorRef: RefObject<ContentEditorRef | null>,
): CommentUploads {
  const storeUploads = useCommentDraftStore((s) =>
    draftKey ? s.getUploads(draftKey) : EMPTY_UPLOADS,
  );

  // 每个 key 有引用稳定的绑定——settle 闭包会捕获它、且在本组件卸载后仍需
  // 继续工作，因此所有访问器都走 getState()。
  const binding = useMemo<UploadDraftBinding | undefined>(() => {
    if (!draftKey) return undefined;
    return {
      registryKey: `comment:${draftKey}`,
      getUploads: () => useCommentDraftStore.getState().getUploads(draftKey),
      addUpload: (u) => useCommentDraftStore.getState().addUpload(draftKey, u),
      settleUpload: (id, att) => useCommentDraftStore.getState().settleUpload(draftKey, id, att),
      failUpload: (id, err) => useCommentDraftStore.getState().failUpload(draftKey, id, err),
      removeUpload: (id) => useCommentDraftStore.getState().removeUpload(draftKey, id),
      getBody: () => useCommentDraftStore.getState().getDraft(draftKey) ?? "",
      appendToBody: (md) => useCommentDraftStore.getState().appendToDraftContent(draftKey, md),
    };
  }, [draftKey]);

  return useCoordinatedUploads(binding, storeUploads, ctx, editorGate, editorRef);
}
