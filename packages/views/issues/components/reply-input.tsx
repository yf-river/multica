"use client";

// 本文件：ReplyInput —— 评论楼中楼的"回复"输入框（区别于顶层评论输入框）。
// 与 CommentInput 共用同一套编辑器/上传/发送 hook 与草稿语义，差异点在于：
// 回复完成后保留光标（供连续回复）、通常带发送者头像、可传 `draftKey`
// 在虚拟化时间线里跨卸载持久化。与顶层输入框的关键差异一律"见 CommentInput"。
// （MUL-5588 / MUL-5181）

import { useRef, useState, useCallback, useEffect } from "react";
import { ContentEditor, type ContentEditorRef, useFileDropZone, FileDropOverlay, useLazyEditor, useUploadGate, useComposerSubmit } from "../../editor";
import { FileUploadButton } from "@multica/ui/components/common/file-upload-button";
import { SubmitButton } from "@multica/ui/components/common/submit-button";
import { ActorAvatar } from "../../common/actor-avatar";
import { contentReferencesAttachment } from "@multica/core/types";
import { formatShortcut, useShortcut } from "@multica/core/shortcuts";
import { useCommentDraftStore, type CommentDraftKey } from "@multica/core/issues/stores";
import { cn } from "@multica/ui/lib/utils";
import type { AvatarSize } from "@multica/ui/lib/avatar-size";
import { useT } from "../../i18n";
import { CommentTriggerChips } from "./comment-trigger-chips";
import { useCommentTriggerPreview } from "../hooks/use-comment-trigger-preview";
import { useCommentUploads } from "./use-comment-uploads";
import { useQuickActionMenu } from "../hooks/use-quick-action-menu";

// ---------------------------------------------------------------------------
// 类型定义
// ---------------------------------------------------------------------------

/**
 * 回复输入框的 props（差异点说明见各成员）。
 */
interface ReplyInputProps {
  issueId: string;
  parentId: string;
  placeholder?: string;
  avatarType: string;
  avatarId: string;
  /** 成功返回 true、失败返回 false——在此之前回复框保持文本（锁定 + 旋转），
   *  只有成功才清空。 */
  onSubmit: (content: string, attachmentIds?: string[], suppressAgentIds?: string[]) => Promise<string | boolean>;
  /** 服务端接受回复、输入框被清空后被调用。 */
  onAccepted?: (commentId: string) => void;
  size?: "sm" | "default";
  /** 设置后，进行中的回复会经草稿 store 水合/持久化。
   *  虚拟化时间线线程里的回复必须传——那里的 CommentCard 可能因滚出而卸载。 */
  draftKey?: CommentDraftKey;
}

// ---------------------------------------------------------------------------
// ReplyInput 组件
// ---------------------------------------------------------------------------

/**
 * 楼中楼"回复"输入框（职责见文件头）。
 *
 * `issueId` + `parentId` 标定回复落点；`avatarType`/`avatarId` 决定发送者
 * 头像；`draftKey` 存在时把进行中草稿跨卸载持久化（虚拟化线程必需）；
 * `size` 切换紧凑/常规两档。
 */
function ReplyInput({
  issueId,
  parentId,
  placeholder,
  avatarType,
  avatarId,
  onSubmit,
  onAccepted,
  size = "default",
  draftKey,
}: ReplyInputProps) {
  const { t } = useT("issues");
  const { t: tEditor } = useT("editor");
  const sendShortcut = useShortcut("send");
  const placeholderText = placeholder ?? t(($) => $.reply.placeholder);
  const editorRef = useRef<ContentEditorRef>(null);
  const composerRef = useRef<HTMLDivElement>(null);
  // 见 CommentInput——回复时中途上传同样会把文件发丢。
  const uploadGate = useUploadGate(editorRef);
  // `/` 菜单里的快捷动作——与顶层输入框同一个目录、同样的"插入而不立即触发"
  // 行为。回复投给同一条 issue，所以这里 `/` 必须提供同样的能力（MUL-5588）。
  const quickActionMenu = useQuickActionMenu(issueId);
  // 若给了草稿 key，挂载时从 store 水合（defaultValue 是 ContentEditorRef
  // 唯一注入点），并在每次 onUpdate 时冲刷。
  const [initialDraft] = useState(() =>
    draftKey ? useCommentDraftStore.getState().getDraft(draftKey) : undefined,
  );
  const [content, setContent] = useState(initialDraft ?? "");
  const setDraft = useCommentDraftStore((s) => s.setDraft);
  const [isEmpty, setIsEmpty] = useState(!initialDraft?.trim());
  const [suppressedAgentIds, setSuppressedAgentIds] = useState<Set<string>>(() => new Set());
  const triggerPreview = useCommentTriggerPreview({ issueId, parentId, content });
  // 本次回复会话的上传（MUL-5181）——归协调器所有。带 draftKey 时持久化在
  // 草稿 store 里，滚出/关闭不再丢掉在途上传；不带（无持久化上下文）时回退
  // 到 hook 内部的会话局部状态。
  // `gate` 用协调器持有的占位符扩宽编辑器门禁——见 CommentInput。
  const { uploads, attachments: pendingAttachments, handleUpload, removeUpload, gate } =
    useCommentUploads(draftKey, { issueId }, uploadGate, editorRef);

  // 只读优先：意图前为静态外壳；有未发送草稿则立即挂载真正编辑器
  //（见 CommentInput）。这也是回复框能穿越 Virtuoso 滚出仍可用的原因——
  // 打过的草稿在卡片重挂时重新水合成活编辑器，未触碰的空框则折回外壳。
  const lazy = useLazyEditor({
    initialActive:
      !!initialDraft?.trim() ||
      (draftKey ? useCommentDraftStore.getState().getUploads(draftKey).length > 0 : false),
    editorRef,
  });
  const { isDragOver, dropZoneProps } = useFileDropZone({
    onDrop: lazy.uploadOrQueue,
  });

  // 关标签页/手机切后台时冲刷——理由同 CommentInput。
  useEffect(() => {
    if (!draftKey) return;
    const flush = () => {
      const md = editorRef.current?.getMarkdown();
      if (md && md.trim().length > 0) setDraft(draftKey, md);
    };
    const onVis = () => { if (document.visibilityState === "hidden") flush(); };
    document.addEventListener("visibilitychange", onVis);
    window.addEventListener("pagehide", flush);
    return () => {
      document.removeEventListener("visibilitychange", onVis);
      window.removeEventListener("pagehide", flush);
    };
  }, [draftKey, setDraft]);

  useEffect(() => {
    setSuppressedAgentIds(new Set());
  }, [issueId, parentId]);

  useEffect(() => {
    const visible = new Set(triggerPreview.agents.map((agent) => agent.id));
    setSuppressedAgentIds((prev) => {
      const next = new Set([...prev].filter((id) => visible.has(id)));
      return next.size === prev.size ? prev : next;
    });
  }, [triggerPreview.agents]);

  const toggleSuppressedAgent = useCallback((agentId: string) => {
    setSuppressedAgentIds((prev) => {
      const next = new Set(prev);
      if (next.has(agentId)) next.delete(agentId);
      else next.add(agentId);
      return next;
    });
  }, []);

  // 先等待再渲染的发送（见 CommentInput）：共享 hook 保留文本、锁定 + 旋转，
  // 并且只在服务端接受后才清空。
  // 过期提交守卫——见 CommentInput。
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  const submittedEntryRef = useRef<unknown>(null);
  // 见 CommentInput：绑定在真正清空了编辑器的那个分支上，因此被过期提交
  // 守卫保留的草稿绝不会被打扰。
  const editorScrubbedRef = useRef(false);
  const acceptedCommentIdRef = useRef<string | null>(null);

  const { submitting, submit } = useComposerSubmit({
    editorRef,
    uploadGate: gate,
    containerRef: composerRef,
    // 楼中楼回复通常不是用户最后要说的话，所以光标留在框里等下一句。
    // 与顶层评论不同，发出的回复正好落在仍在聚焦的输入框正上方——无需把
    // 视线引向别处。`containerRef` 用于在请求在途期间用户把焦点移到另一个
    // 输入框时不抢焦点。
    afterAccepted: () => (editorScrubbedRef.current ? "refocus" : "none"),
    onSubmit: (content) => {
      editorScrubbedRef.current = false;
      if (draftKey) {
        // 快照前先冲刷积压的防抖——见 CommentInput。
        const pending = editorRef.current?.flushPendingUpdate?.();
        if (pending != null) setDraft(draftKey, pending);
        submittedEntryRef.current = useCommentDraftStore.getState().drafts[draftKey];
      }
      // 只绑定正文仍引用的上传（见 CommentInput）：删除内联图片就真的解绑；
      // 关闭后幸存的上传由 settle 处理器写回正文。
      const activeIds = pendingAttachments
        .filter((a) => contentReferencesAttachment(content, a))
        .map((a) => a.id);
      const suppressAgentIds = triggerPreview.agents
        .filter((agent) => suppressedAgentIds.has(agent.id))
        .map((agent) => agent.id);
      return onSubmit(
        content,
        activeIds.length > 0 ? activeIds : undefined,
        suppressAgentIds.length > 0 ? suppressAgentIds : undefined,
      ).then((commentId) => {
        acceptedCommentIdRef.current = typeof commentId === "string" ? commentId : null;
        return !!commentId;
      });
    },
    onAccepted: () => {
      // 成功只能消费它自己提交的那份条目——见 CommentInput。
      if (draftKey) {
        const lateMd = editorRef.current?.flushPendingUpdate?.();
        if (lateMd != null) setDraft(draftKey, lateMd);
        const store = useCommentDraftStore.getState();
        const live = store.drafts[draftKey];
        const untouched = live === undefined || live === submittedEntryRef.current;
        if (untouched) store.clearDraft(draftKey);
        if (!mountedRef.current || !untouched) return;
      } else {
        if (!mountedRef.current) return;
        uploads.forEach((u) => removeUpload(u.clientUploadId));
      }
      editorRef.current?.clearContent();
      setContent("");
      setIsEmpty(true);
      setSuppressedAgentIds(new Set());
      editorScrubbedRef.current = true;
      if (acceptedCommentIdRef.current) onAccepted?.(acceptedCommentIdRef.current);
    },
  });

  const avatarSize: AvatarSize = size === "sm" ? "sm" : "md";

  return (
    <div className="group/editor flex items-start gap-2.5">
      <ActorAvatar
        actorType={avatarType}
        actorId={avatarId}
        size={avatarSize}
        className="mt-0.5 shrink-0"
      />
      <div
        {...dropZoneProps}
        ref={composerRef}
        className={cn(
          "relative min-w-0 flex-1 flex flex-col",
          !isEmpty && "pb-9",
        )}
      >
        {/* 回复在途时锁死编辑器——见 CommentInput。 */}
        {lazy.active && (
        <div
          className={cn(
            "flex-1 min-h-0 overflow-y-auto",
            submitting && "pointer-events-none opacity-60",
            !lazy.ready && "hidden",
          )}
          aria-busy={submitting || undefined}
        >
          <ContentEditor
            ref={editorRef}
            defaultValue={initialDraft}
            onReady={lazy.onReady}
            placeholder={placeholderText}
            onUpdate={(md) => {
              setContent(md);
              setIsEmpty(!md.trim());
              // setDraft 保留任何在途附件，只在文本与附件都为空时才丢弃条目。
              if (draftKey) setDraft(draftKey, md);
            }}
            onSubmit={submit}
            onUploadFile={handleUpload}
            onUploadingChange={uploadGate.onUploadingChange}
            debounceMs={100}
            currentIssueId={issueId}
            attachments={pendingAttachments}
            enableSlashCommands
            slashCommandMode="command"
            quickActionMenu={quickActionMenu}
          />
        </div>
        )}
        {/* 静态外壳——克隆空的单行回复框（模式见 CommentInput）。 */}
        {!lazy.ready && (
          <div
            data-testid="reply-composer-shell"
            role="button"
            tabIndex={0}
            aria-label={placeholderText}
            className="flex-1 min-h-0 cursor-text rich-text-editor text-body"
            onClick={() => lazy.activate()}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                lazy.activate();
              }
            }}
          >
            {/* rich-text-editor 下的 <p>：与真正编辑器空段落的字模度量一致——
                切换时不会有高度跳动。 */}
            <p className="text-muted-foreground">{placeholderText}</p>
          </div>
        )}
        <div className="absolute bottom-0 left-0 right-24 min-w-0">
          <CommentTriggerChips
            agents={triggerPreview.agents}
            blocked={triggerPreview.blocked}
            draftContent={content}
            suppressedAgentIds={suppressedAgentIds}
            onToggle={toggleSuppressedAgent}
          />
        </div>
        <div className="absolute bottom-0 right-0 flex items-center gap-1">
          <FileUploadButton
            size="sm"
            multiple
            onSelect={(file) => lazy.uploadOrQueue([file])}
          />
          <SubmitButton
            onClick={submit}
            disabled={isEmpty}
            loading={submitting}
            busy={gate.uploading}
            tooltip={gate.uploading
              ? tEditor(($) => $.upload.in_progress)
              : sendShortcut
                ? `${t(($) => $.comment.send_tooltip)} · ${formatShortcut(sendShortcut)}`
                : t(($) => $.comment.send_tooltip)}
            ariaLabel={gate.uploading
              ? tEditor(($) => $.upload.in_progress)
              : t(($) => $.comment.send_tooltip)}
          />
        </div>
        {isDragOver && <FileDropOverlay />}
      </div>
    </div>
  );
}

export { ReplyInput, type ReplyInputProps };
