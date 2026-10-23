"use client";

// 本文件：CommentInput —— 评论发布输入框（顶级评论，非楼中楼回复）。
// 职责：草稿持久化、懒挂载编辑器、图片上传与发送门禁、@"agent"触发预览与
// 抑制、发送期间的锁定/旋转与"仅成功后清空"语义。回复框见 reply-input.tsx，
// 两者共享同一批编辑器 hook，关键差异用"见 CommentInput"互相指路。

import { useRef, useState, useCallback, useEffect } from "react";
import { cn } from "@multica/ui/lib/utils";
import { ContentEditor, type ContentEditorRef, useFileDropZone, FileDropOverlay, useLazyEditor, useUploadGate, useComposerSubmit } from "../../editor";
import { FileUploadButton } from "@multica/ui/components/common/file-upload-button";
import { SubmitButton } from "@multica/ui/components/common/submit-button";
import { contentReferencesAttachment } from "@multica/core/types";
import { formatShortcut, useShortcut } from "@multica/core/shortcuts";
import { useCommentDraftStore } from "@multica/core/issues/stores";
import { useT } from "../../i18n";
import { CommentTriggerChips } from "./comment-trigger-chips";
import { useCommentTriggerPreview } from "../hooks/use-comment-trigger-preview";
import { useCommentUploads } from "./use-comment-uploads";
import { useQuickActionMenu } from "../hooks/use-quick-action-menu";
import { useStickyComposer } from "../hooks/use-sticky-composer";

interface CommentInputProps {
  issueId: string;
  /** 成功返回 true、失败返回 false。此 promise 未落定前发送框保持文本
   *  （编辑器锁定 + 按钮旋转）；只有成功才清空——发送失败绝不能默默丢掉
   *   用户草稿。 */
  onSubmit: (content: string, attachmentIds?: string[], suppressAgentIds?: string[]) => Promise<string | boolean>;
  /** 服务端接受评论、发送框清空后被调用。 */
  onAccepted?: (commentId: string) => void;
}

/**
 * 顶级评论发布输入框（职责见文件头）。
 *
 * `issueId` 指定评论所属问题并决定草稿 key 与触发预览的查询范围；
 * `onSubmit` 返回"是否成功"的 promise，决定清空还是保留草稿；
 * `onAccepted` 在服务端接受评论、编辑器被清空后回调（携带新评论 id）。
 */
function CommentInput({ issueId, onSubmit, onAccepted }: CommentInputProps) {
  const { t } = useT("issues");
  const { t: tEditor } = useT("editor");
  const sendShortcut = useShortcut("send");
  const editorRef = useRef<ContentEditorRef>(null);
  // 上传中途发送会把仍在途图片的 blob URL 从 markdown 里剥掉，又没绑上
  // 附件 id——评论发出后没有那张文件。门禁防止这种情况。
  const uploadGate = useUploadGate(editorRef);
  // 挂载时读一次已持久化的草稿。ContentEditor 只在挂载时认 `defaultValue`，
  // 所以这份快照同时驱动编辑器的初始内容与发送按钮的可用态——没有它，
  // 编辑器里明明有字，按钮却会是禁用状态。
  // `/` 菜单里的快捷动作：选一个会把服务端渲染好的正文插入，让用户发送前
  // 还能编辑，而不是立即触发。
  const quickActionMenu = useQuickActionMenu(issueId);
  const draftKey = `new:${issueId}` as const;
  const [initialDraft] = useState(() =>
    useCommentDraftStore.getState().getDraft(draftKey),
  );
  const [content, setContent] = useState(initialDraft ?? "");
  const [isEmpty, setIsEmpty] = useState(() => !initialDraft?.trim());
  const [suppressedAgentIds, setSuppressedAgentIds] = useState<Set<string>>(() => new Set());
  const triggerPreview = useCommentTriggerPreview({ issueId, content });
  // 本次输入会话的上传（MUL-5181）。归模块级协调器所有，并持久化在草稿
  // store 里——关闭/滚离输入框不再丢掉在途上传：结果会落进草稿。
  // `attachments`（已完成行）同时驱动提交时的 `attachment_ids` 载荷和编辑器的
  // AttachmentDownloadProvider；`uploads` 驱动状态 chip（上传中/失败/中断）。
  // `gate` 用协调器持有的占位符扩宽编辑器的门禁，因此重开输入框叠在仍在途的
  // 上传之上时，也不可能越过门禁发出去。
  const { attachments: pendingAttachments, handleUpload, gate } =
    useCommentUploads(draftKey, { issueId }, uploadGate, editorRef);

  // 只读优先：在用户表现出意图（点击/键盘/拖文件）之前，输入框渲染成
  // 外观一致的静态外壳。未发送的草稿本身就是一种常驻意图——直接挂载真正的
  // 编辑器，让草稿立即可见可编辑，与去懒加载化之前的行为一致。
  const lazy = useLazyEditor({
    initialActive:
      !!initialDraft?.trim() ||
      useCommentDraftStore.getState().getUploads(draftKey).length > 0,
    editorRef,
  });
  const { isDragOver, dropZoneProps } = useFileDropZone({
    onDrop: lazy.uploadOrQueue,
  });
  // 吸顶偏好（设置 → 偏好）：开启时，issue detail 把本输入框钉在滚动
  // 视口底部。与宿主共享同一状态，保证下面的高度上限永远不会比钉住更长久。
  const sticky = useStickyComposer();

  // 草稿持久化。挂载时通过上面的 `defaultValue` 从 store 注入
  //（ContentEditorRef 没有 setContent，这是唯一的注入点）。
  // 每次 onUpdate（上游已做防抖）+ visibilitychange/pagehide 都冲刷一次，
  // 因此关标签页/手机切后台都不会丢内容。提交成功后清空。
  const setDraft = useCommentDraftStore((s) => s.setDraft);
  useEffect(() => {
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
  }, [issueId]);

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

  // 先等待再渲染的发送（MUL-5181）：共享 hook 负责读 markdown、拦截空内容/
  // 在途、重查上传门禁，通过 `submitting` 锁定 + 旋转，并且只在服务端接受后
  // 才清空——发送失败保留草稿，而不是默默丢弃。
  // 过期提交守卫（MUL-5181 P0）：若本输入框在提交中途卸载（关闭 issue
  // detail），用户重开并在同 key 下打出新草稿，迟到的成功只会清掉它自己
  // 提交的那份草稿。
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  const submittedEntryRef = useRef<unknown>(null);
  // 只在真正清空了编辑器的那个分支里由 `onAccepted` 置位；`afterAccepted`
  // 读回它。之所以这样绑定，是因为过期提交守卫可能让一份半途草稿留在原地：
  // 此时若落下光标，会把输入焦点从用户正在打的句子里抽走。
  const editorScrubbedRef = useRef(false);
  const acceptedCommentIdRef = useRef<string | null>(null);

  const { submitting, submit } = useComposerSubmit({
    editorRef,
    uploadGate: gate,
    // 顶层评论收束一条回合：落下光标而不保留，让评论一旦发出，输入框不再
    // 读作"还在写"。楼中楼回复相反——见 ReplyInput。
    afterAccepted: () => (editorScrubbedRef.current ? "blur" : "none"),
    onSubmit: (content) => {
      editorScrubbedRef.current = false;
      // 快照前先冲刷编辑器积压的防抖——提交前打字触发的那次迟到冲刷，
      // 不能读成"请求期间的编辑"。
      const pending = editorRef.current?.flushPendingUpdate?.();
      if (pending != null) setDraft(draftKey, pending);
      submittedEntryRef.current = useCommentDraftStore.getState().drafts[draftKey];
      // 只绑定正文仍然引用的上传（MUL-5181）：删除内联图片就真的解绑。
      // 关闭后完成的上传由 settle 处理器写回正文，所以幸存的文件同样有引用
      //——绝不静默附上。
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
      // 成功只能消费它自己提交的那份条目（MUL-5181 P0）：请求在途期间做
      // 的编辑——或本输入框卸载后重开编辑器所做的编辑——在 store 和编辑器
      // 里都能存活。先冲刷积压的防抖，好正确判断窗口内的打字（空冲刷保持
      // 条目身份不变）。
      const lateMd = editorRef.current?.flushPendingUpdate?.();
      if (lateMd != null) setDraft(draftKey, lateMd);
      const store = useCommentDraftStore.getState();
      const live = store.drafts[draftKey];
      const untouched = live === undefined || live === submittedEntryRef.current;
      if (untouched) store.clearDraft(draftKey);
      if (!mountedRef.current || !untouched) return;
      editorRef.current?.clearContent();
      setContent("");
      setIsEmpty(true);
      setSuppressedAgentIds(new Set());
      editorScrubbedRef.current = true;
      if (acceptedCommentIdRef.current) onAccepted?.(acceptedCommentIdRef.current);
    },
  });

  return (
    <div
      {...dropZoneProps}
      className="relative flex flex-col rounded-lg bg-card pb-8 ring-1 ring-border"
    >
      {/* 发送在途时锁死编辑器。ContentEditor 挂载后无法切换 Tiptap 的
          `editable`（见其 docstring），所以让它不可交互的官方做法是
          pointer-events-none + 变暗的外层包装。 */}
      {lazy.active && (
      <div
        className={cn(
          "flex-1 min-h-0 overflow-y-auto px-3 py-2",
          // 钉在视口底部后输入框向上生长；加个上限，长草稿不能吞掉整条
          // 时间线（编辑器区域内部滚动）。
          sticky && "max-h-[40vh]",
          submitting && "pointer-events-none opacity-60",
          !lazy.ready && "hidden",
        )}
        aria-busy={submitting || undefined}
      >
        <ContentEditor
          ref={editorRef}
          defaultValue={initialDraft}
          onReady={lazy.onReady}
          placeholder={t(($) => $.comment.leave_comment_placeholder)}
          onUpdate={(md) => {
            setContent(md);
            setIsEmpty(!md.trim());
            // 上游已防抖（debounceMs=100）。每个 tick 都持久化，这样刷新或
            // 滚出视口都能把工作恢复到按键粒度。setDraft 保留任何在途附件，
            // 只在文本与附件都为空时才丢弃条目。
            setDraft(draftKey, md);
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
      {/* 静态外壳——视觉上克隆空的单行输入框。真正编辑器在首次交互时
          （隐藏地）挂载；外壳保持可见直到它就绪，卡片因此从不空白或跳动。 */}
      {!lazy.ready && (
        <div
          data-testid="comment-composer-shell"
          role="button"
          tabIndex={0}
          aria-label={t(($) => $.comment.leave_comment_placeholder)}
          className="flex-1 min-h-0 cursor-text px-3 py-2"
          onClick={() => lazy.activate()}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              lazy.activate();
            }
          }}
        >
          {/* rich-text-editor + <p>：外壳行继承编辑器的精确字模度量
              （prose.css 里 line-height 1.625），外壳→编辑器的切换因此
              不会引起布局跳动。 */}
          <div className="rich-text-editor text-body">
            <p className="text-muted-foreground">{t(($) => $.comment.leave_comment_placeholder)}</p>
          </div>
        </div>
      )}
      <div className="absolute bottom-1 left-2 right-28 min-w-0">
        <CommentTriggerChips
          agents={triggerPreview.agents}
          blocked={triggerPreview.blocked}
          draftContent={content}
          reviewReply={triggerPreview.reviewReply}
          suppressedAgentIds={suppressedAgentIds}
          onToggle={toggleSuppressedAgent}
        />
      </div>
      <div className="absolute bottom-1 right-1.5 flex items-center gap-1">
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
  );
}

export { CommentInput };
