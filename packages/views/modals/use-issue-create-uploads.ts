// 本文件职责：把「协调式上传引擎」绑定到 issue 创建流程（MUL-5181, L2），
// 暴露 useIssueCreateUploads 供手动/Agent 两个创建面板共用同一份上传池。
"use client";

/**
 * issue 创建流程对「协调式上传引擎」（MUL-5181, L2）的绑定。
 *
 * 手动表单和 Agent prompt 两个创建面板共享同一个上传池——`draft.shared.attachments`——
 * 所以无论从哪一侧开始上传，文件都能跨「手动 ↔ Agent」模式切换存活。两侧的差异只在于
 * 上传链接要写回哪个 BODY：手动描述还是 Agent prompt。该绑定记住「发起上传时的模式」，
 * 这样即使用户关了弹窗（或中途切了模式）之后上传才完成，链接也只会写回当初粘贴它的
 * 那个正文，而不会写到另一侧。
 *
 * 这里的上传不携带 issue/评论上下文——issue 还不存在；服务端在创建时才通过
 * `attachment_ids` 把附件真正挂上。
 */

import { useMemo, type RefObject } from "react";
import { attachmentToDraftUpload, type DraftUpload } from "@multica/core/drafts";
import { useIssueDraftStore } from "@multica/core/issues/stores";
import type { CreateMode } from "@multica/core/issues/stores";
import type { UploadGate } from "../editor/use-upload-gate";
import type { ContentEditorRef } from "../editor/content-editor";
import {
  useCoordinatedUploads,
  type CoordinatedUploads,
  type UploadDraftBinding,
} from "../editor/use-coordinated-uploads";

/** 把一段 markdown 追加到已有正文末尾（正文非空时用空行分隔，并去掉多余的行尾空白）。 */
function appendMarkdown(existing: string, markdown: string): string {
  return existing.trim() ? `${existing.replace(/\s+$/, "")}\n\n${markdown}` : markdown;
}

function sharedUploads(): DraftUpload[] {
  return useIssueDraftStore.getState().draft.shared.attachments ?? [];
}

function setSharedUploads(next: DraftUpload[]): void {
  useIssueDraftStore.getState().setShared({ attachments: next });
}

/**
 * 获取「按当前创建模式绑定的」上传协调器。
 *
 * 上传池是共享草稿里的 `shared.attachments`，但上传完成的 markdown 链接要写回
 * 发起上传时所在模式的正文（manual → 描述，agent → prompt），绑定即实现该隔离。
 *
 * @param mode       当前创建模式（manual / agent），决定链接写回哪个正文
 * @param editorGate 编辑器自己的上传闸门，作为协调器的外置闸门一并生效
 * @param editorRef  对应编辑器的 ref，供协调器触发 flush 等操作
 */
export function useIssueCreateUploads(
  mode: CreateMode,
  editorGate: UploadGate,
  editorRef: RefObject<ContentEditorRef | null>,
): CoordinatedUploads {
  const uploads = useIssueDraftStore((s) => s.draft.shared.attachments);

  const binding = useMemo<UploadDraftBinding>(
    () => ({
      registryKey: `issue-create:${mode}`,
      getUploads: sharedUploads,
      addUpload: (u) => {
        const cur = sharedUploads();
        if (cur.some((x) => x.clientUploadId === u.clientUploadId)) return;
        setSharedUploads([...cur, u]);
      },
      settleUpload: (id, att) => {
        const cur = sharedUploads();
        if (!cur.some((u) => u.clientUploadId === id)) return;
        // attachmentToDraftUpload 会在记录落库前剥掉响应里那个带作用域的
        // signed download_url，避免把临时签名地址持久化。
        setSharedUploads(
          cur.map((u) =>
            u.clientUploadId === id ? { ...attachmentToDraftUpload(att), clientUploadId: id } : u,
          ),
        );
      },
      failUpload: (id, error) => {
        const cur = sharedUploads();
        const target = cur.find((u) => u.clientUploadId === id);
        if (!target) return;
        setSharedUploads(
          cur.map((u) =>
            u.clientUploadId === id
              ? {
                  clientUploadId: id,
                  status: "failed",
                  filename: target.filename,
                  size: target.size,
                  contentType: target.contentType,
                  error,
                }
              : u,
          ),
        );
      },
      removeUpload: (id) => {
        const cur = sharedUploads();
        if (!cur.some((u) => u.clientUploadId === id)) return;
        setSharedUploads(cur.filter((u) => u.clientUploadId !== id));
      },
      getBody: () => {
        const { draft } = useIssueDraftStore.getState();
        return mode === "agent" ? draft.agent.prompt : draft.manual.description;
      },
      appendToBody: (md) => {
        const state = useIssueDraftStore.getState();
        if (mode === "agent") {
          state.setAgent({ prompt: appendMarkdown(state.draft.agent.prompt, md) });
        } else {
          state.setManual({ description: appendMarkdown(state.draft.manual.description, md) });
        }
      },
    }),
    [mode],
  );

  return useCoordinatedUploads(binding, uploads, {}, editorGate, editorRef);
}
