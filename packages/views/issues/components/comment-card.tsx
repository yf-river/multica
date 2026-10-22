"use client";

// 本文件：CommentCard —— issue 详情时间线里的"每条顶层评论一张卡片"。
// 一张卡片容纳：根评论（作者/时间/折叠开关/更多菜单）+ 按时间排列的回复行 + 底部回复
// 输入框，以及"回复即解决"时的折叠横条与绿色徽标。支持编辑（带附件重新绑定与修订
// 冲突）、删除确认、复制文本、评论触发（@ agent）/ 反应、把评论转成子任务等。
// 通过 React.memo 保证长时间线（上千条评论）下，无关状态/WS 刷新不会让每张卡都重渲染。
// 根评论与回复各自用 sticky 表头，滚动长正文时作者与操作保持可见（详见各注释）。

import { memo, useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { CheckCircle2, ChevronRight, ListChevronsDownUp, Copy, Loader2, MessageSquarePlus, MoreHorizontal, Pencil, RotateCcw, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Card } from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@multica/ui/components/ui/dropdown-menu";
import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { ActorAvatar } from "../../common/actor-avatar";
import { ReactionBar } from "@multica/ui/components/common/reaction-bar";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { useActorName } from "@multica/core/workspace/hooks";
import { useLocale, useTimeAgo } from "../../i18n";
import { ContentEditor, type ContentEditorRef, ReadonlyContent, useFileDropZone, FileDropOverlay, Attachment as AttachmentRenderer, AttachmentDownloadProvider, useUploadGate, useComposerSubmit } from "../../editor";
import { useCommentUploads } from "./use-comment-uploads";
import { FileUploadButton } from "@multica/ui/components/common/file-upload-button";
import { api, dispatchReasonCode, errorCode } from "@multica/core/api";
import { ReplyInput } from "./reply-input";
import { CommentTriggerChips } from "./comment-trigger-chips";
import { useCommentTriggerPreview } from "../hooks/use-comment-trigger-preview";
import type { TimelineEntry, Attachment } from "@multica/core/types";
import { contentReferencesAttachment } from "@multica/core/types";
import { selectStandaloneAttachments } from "@multica/core/attachments/image-sequence";
import { useCommentCollapseStore, useCommentDraftStore } from "@multica/core/issues/stores";
import { useT } from "../../i18n";
import { CommentsFoldBar } from "./resolved-thread-bar";
import { RunDiffCard } from "./run-diff-card";
import { deriveThreadResolution } from "./thread-utils";
import { RevisionConflictCompare } from "./revision-conflict-compare";

const highlightedCommentBackgroundClass =
  "bg-[color-mix(in_srgb,var(--card)_95%,var(--brand)_5%)]";
const stickyHeaderFadeClass =
  "after:pointer-events-none after:absolute after:inset-x-0 after:top-full after:h-1 after:bg-[inherit] after:[mask-image:linear-gradient(to_bottom,#000,transparent)] after:[-webkit-mask-image:linear-gradient(to_bottom,#000,transparent)]";

function StickyHeaderShell({
  className,
  sticky = true,
  highlighted,
  children,
}: {
  className?: string;
  sticky?: boolean;
  highlighted?: boolean;
  children: ReactNode;
}) {
  if (!sticky) {
    return (
      <div className={cn(highlighted && highlightedCommentBackgroundClass, className)}>
        {children}
      </div>
    );
  }

  return (
    <div
      className={cn(
        "sticky top-0 z-10 transition-colors duration-700",
        !highlighted && stickyHeaderFadeClass,
        highlighted ? highlightedCommentBackgroundClass : "bg-card",
        className,
      )}
    >
      {children}
    </div>
  );
}

// ---------------------------------------------------------------------------
// 类型定义
// ---------------------------------------------------------------------------

interface CommentCardProps {
  issueId: string;
  entry: TimelineEntry;
  /**
   * 该线程根下所有嵌套回复的扁平列表，按渲染顺序排列。
   * 在 `issue-detail.tsx` 的 `timelineView` 里算一次并做稳定化，保证只有*本条线程*
   * 的回复变化时数组引用才变化——无关线程来了条新回复绝不能击穿本卡的 memo。
   * 以前直接传整个 Map 正是那样。
   */
  replies: TimelineEntry[];
  currentUserId?: string;
  /**
   * 当前用户是否为工作区 owner/admin——是则可审核任何人写的评论，恢复后端在
   * `comment.go:507-512` 已授予的管理员覆盖。在 `issue-detail.tsx` 算一次并一路
   * 传下来，避免本组件与 `CommentRow` 每行都重算这条规则。
   */
  canModerate?: boolean;
  onReply: (parentId: string, content: string, attachmentIds?: string[], suppressAgentIds?: string[]) => Promise<string | boolean>;
  onReplyAccepted?: (commentId: string) => void;
  onEdit: (commentId: string, content: string, attachmentIds: string[], suppressAgentIds?: string[], contentBase?: string) => Promise<void>;
  onDelete: (commentId: string) => void;
  onToggleReaction: (commentId: string, emoji: string) => void;
  onCreateSubIssue?: (commentId: string) => void;
  /** 解决/取消解决线程里的任何评论（commentId = 目标行）。 */
  onResolveToggle?: (commentId: string, resolved: boolean) => void;
  /**
   * 非空时，线程根当前以"已解决但已展开"的卡片渲染。把"折叠"入口交给表头，
   * 让用户能把线程折回横条；该会话状态由父组件持有。
   */
  onCollapseResolved?: () => void;
  /**
   * 会话级的线程 ROOT id 集合：其中"回复即解决"的折叠处于展开态。
   * 仅在*回复*是解决者时用到（根解决者的折叠在 issue-detail 的 resolved-bar 层处理）。
   * 以 root id 为键。
   */
  expandedResolvedIds?: ReadonlySet<string>;
  onResolvedExpandChange?: (rootId: string, expand: boolean) => void;
  /** 要高亮（闪烁动画）的评论 id。 */
  highlightedCommentId?: string | null;
  /**
   * 1. task id → id of that run's newest comment. The run's diff card is rendered
   * only here: the snapshot belongs to the run, so a run that posts several
   * comments must not repeat it. Computed once over the whole timeline
   * (`lastCommentIdByTaskId` in `issue-detail.tsx`), never per card.
   */
  runDiffCommentIds?: ReadonlyMap<string, string>;
}

// ---------------------------------------------------------------------------
// 共用的删除确认对话框
// ---------------------------------------------------------------------------

function DeleteCommentDialog({
  open,
  onOpenChange,
  onConfirm,
  hasReplies,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
  hasReplies?: boolean;
}) {
  const { t } = useT("issues");
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(($) => $.comment.delete_title)}</AlertDialogTitle>
          <AlertDialogDescription>
            {hasReplies
              ? t(($) => $.comment.delete_desc_with_replies)
              : t(($) => $.comment.delete_desc)}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t(($) => $.comment.cancel_action)}</AlertDialogCancel>
          <AlertDialogAction variant="destructive" onClick={onConfirm}>
            {t(($) => $.comment.delete_action)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// ---------------------------------------------------------------------------
// 独立的附件列表——渲染尚未被 markdown 正文引用的附件
// ---------------------------------------------------------------------------

/**
 * 独立的附件列表——渲染那些尚未被 markdown 正文引用的附件。通常挂在
 * CommentRow / 回复 / 根评论正文下方。
 */
export function AttachmentList({
  attachments,
  content,
  className,
  onRemove,
}: {
  attachments?: Attachment[];
  content?: string;
  className?: string;
  onRemove?: (attachmentId: string) => void;
}) {
  if (!attachments?.length) return null;
  // 跳过"稳定或遗留 URL 已被 markdown 引用"的附件，以及被引用的同名文件的重复项。
  // 与图片序列构建器共享（MUL-5752），保证这里渲染的卡片与查看器翻页的图片不会打架。
  const standalone = selectStandaloneAttachments(content, attachments);
  if (!standalone.length) return null;

  return (
    <AttachmentDownloadProvider attachments={attachments}>
      <div className={cn("flex flex-col gap-1", className)}>
        {standalone.map((a) => (
          <AttachmentRenderer
            key={a.id}
            attachment={{ kind: "record", attachment: a }}
            editable={!!onRemove}
            onDelete={onRemove ? () => onRemove(a.id) : undefined}
          />
        ))}
      </div>
    </AttachmentDownloadProvider>
  );
}

function collectActiveAttachmentIds(
  content: string,
  attachments: Attachment[],
  retainedStandaloneIds?: Set<string> | null,
): string[] {
  const ids = new Set<string>();
  for (const attachment of attachments) {
    if (contentReferencesAttachment(content, attachment)) ids.add(attachment.id);
  }
  for (const id of retainedStandaloneIds ?? []) ids.add(id);
  return [...ids];
}

function sameIdSet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((id) => set.has(id));
}

function initialStandaloneAttachmentIds(entry: TimelineEntry): Set<string> {
  const content = entry.content ?? "";
  return new Set(
    (entry.attachments ?? [])
      .filter((attachment) => !contentReferencesAttachment(content, attachment))
      .map((attachment) => attachment.id),
  );
}

function retryableAgentFailureComment(entry: TimelineEntry): entry is TimelineEntry & { source_task_id: string } {
  return (
    entry.actor_type === "agent" &&
    entry.comment_type === "system" &&
    typeof entry.source_task_id === "string" &&
    entry.source_task_id.length > 0
  );
}

function TaskCommentRetryButton({
  issueId,
  taskId,
  className,
}: {
  issueId: string;
  taskId: string;
  className?: string;
}) {
  const { t } = useT("issues");
  const [retrying, setRetrying] = useState(false);

  const handleRetry = async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      await api.rerunIssue(issueId, taskId);
    } catch (e) {
      // 重跑会复查操作者的调用权限 (MUL-4525)；结构化 403 是权限被挡，
      // 不是瞬时失败。
      toast.error(
        dispatchReasonCode(e) === "invocation_not_allowed"
          ? t(($) => $.execution_log.retry_blocked)
          : e instanceof Error
            ? e.message
            : t(($) => $.execution_log.retry_failed),
      );
    } finally {
      setRetrying(false);
    }
  };

  return (
    <div className={cn("flex", className)}>
      <Button
        type="button"
        size="sm"
        variant="outline"
        onClick={handleRetry}
        disabled={retrying}
        aria-label={t(($) => $.execution_log.retry_task_aria)}
      >
        {retrying ? (
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
        ) : (
          <RotateCcw className="h-3.5 w-3.5" />
        )}
        {t(($) => $.execution_log.retry_task_tooltip)}
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 共用的"编辑附件状态"hook
// ---------------------------------------------------------------------------

function useEditAttachmentState(
  issueId: string,
  entry: TimelineEntry,
  onEdit: (commentId: string, content: string, attachmentIds: string[], suppressAgentIds?: string[], contentBase?: string) => Promise<void>,
) {
  const { t } = useT("issues");
  const { t: tEditor } = useT("editor");
  const [editing, setEditing] = useState(false);
  const [initialValue, setInitialValue] = useState(entry.content ?? "");
  const [initialContentBase, setInitialContentBase] = useState(entry.content ?? "");
  const [revisionConflict, setRevisionConflict] = useState(false);
  const editorRef = useRef<ContentEditorRef>(null);
  // 上传中途保存会先把没有"用户刚粘贴的文件"的编辑持久化——与发新评论是同一个坑。
  const uploadGate = useUploadGate(editorRef);
  const cancelledRef = useRef(false);
  const [content, setContent] = useState(entry.content ?? "");
  const [suppressedAgentIds, setSuppressedAgentIds] = useState<Set<string>>(() => new Set());
  // 本次编辑会话的上传 (MUL-5181)——由协调者持有，并按编辑草稿键存入草稿 store，
  // 使滚出/关闭不再丢掉在途上传。
  const draftKey = `edit:${issueId}:${entry.id}` as const;
  // `gate` 用协调者持有的占位把编辑器的 gate 加宽——见 CommentInput。
  const { uploads, attachments: pendingAttachments, handleUpload, removeUpload, gate } =
    useCommentUploads(draftKey, { issueId }, uploadGate, editorRef);
  const [retainedStandaloneIds, setRetainedStandaloneIds] = useState<Set<string> | null>(null);
  const triggerPreview = useCommentTriggerPreview({
    issueId,
    parentId: entry.parent_id ?? undefined,
    editingCommentId: entry.id,
    content: editing ? content : "",
  });

  const editorAttachments = pendingAttachments.length > 0
    ? [...(entry.attachments ?? []), ...pendingAttachments]
    : entry.attachments;

  useEffect(() => {
    setSuppressedAgentIds(new Set());
  }, [issueId, entry.id, entry.parent_id]);
  useEffect(() => {
    if (revisionConflict) setInitialContentBase(entry.content ?? "");
  }, [entry.content, revisionConflict]);

  const { isDragOver, dropZoneProps } = useFileDropZone({
    onDrop: (files) => files.forEach((f) => editorRef.current?.uploadFile(f)),
    enabled: editing,
  });

  const getDraft = useCommentDraftStore.getState().getDraft;
  const setDraft = useCommentDraftStore((s) => s.setDraft);
  const clearDraft = useCommentDraftStore((s) => s.clearDraft);

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

  const standaloneEditAttachments = (entry.attachments ?? []).filter((a) =>
    retainedStandaloneIds?.has(a.id),
  );

  const resetState = () => {
    setEditing(false);
    setRevisionConflict(false);
    setContent(entry.content ?? "");
    setSuppressedAgentIds(new Set());
    setRetainedStandaloneIds(null);
    // clearDraft 同时清掉编辑文本与其在途附件。
    clearDraft(draftKey);
  };

  const startEdit = () => {
    cancelledRef.current = false;
    const draft = getDraft(draftKey) ?? entry.content ?? "";
    setInitialValue(draft);
    setInitialContentBase(entry.content ?? "");
    setContent(draft);
    setRetainedStandaloneIds(initialStandaloneAttachmentIds(entry));
    setEditing(true);
  };

  const cancelEdit = () => {
    cancelledRef.current = true;
    resetState();
  };

  /**
   * 丢弃本地草稿，改用服务端持有的版本继续。
   * 纯本地操作——服务端本就存着这份内容，因此不写任何东西。
   * 编辑器是脏的（这正是冲突存在的原因），所以 `adoptContent` 是唯一能落地的
   * 通道：改 `defaultValue` 只在挂载时生效。
   */
  const adoptServerVersion = () => {
    const serverContent = entry.content ?? "";
    editorRef.current?.adoptContent(serverContent);
    setContent(serverContent);
    setDraft(draftKey, serverContent);
    setInitialContentBase(serverContent);
    setRevisionConflict(false);
  };

  // "先等再渲染"的保存 (MUL-5181)：共享的提交契约，编辑专属的顾虑都收进 onSubmit——
  // 取消竞争守卫、空操作短路、失败 toast。hook 自身负责空内容守卫、上传复查、
  // 单飞（single-flight）以及 `submitting` 带来的锁/转圈。
  // 过期提交守卫 (MUL-5181 P0)——见 CommentInput。本编辑 hook 活在 CommentRow 里，
  // 所以这里的"卸载"意味着 issue 详情关闭了。
  const editMountedRef = useRef(true);
  useEffect(() => {
    editMountedRef.current = true;
    return () => {
      editMountedRef.current = false;
    };
  }, []);
  const submittedEntryRef = useRef<unknown>(null);

  const { submitting: saving, submit: saveEdit } = useComposerSubmit({
    editorRef,
    uploadGate: gate,
    onSubmit: async (trimmed) => {
      // 保存若与刚按下的取消赛跑，绝不能到达服务端。
      if (cancelledRef.current) return false;
      // 快照前先把挂起的防抖冲掉——见 CommentInput。
      const pendingMd = editorRef.current?.flushPendingUpdate?.();
      if (pendingMd != null) setDraft(draftKey, pendingMd);
      submittedEntryRef.current = useCommentDraftStore.getState().drafts[draftKey];
      const activeIds = collectActiveAttachmentIds(
        trimmed,
        [...(entry.attachments ?? []), ...pendingAttachments],
        // 只算"正文引用 + 保留的独立项" (MUL-5181)：用户从正文里删掉的上传确实是
        // 解绑的。扛过关闭的上传由落定处理器写回正文。
        retainedStandaloneIds,
      );
      const attachmentsChanged = !sameIdSet(activeIds, (entry.attachments ?? []).map((a) => a.id));
      // 什么都没变——不写入，直接关闭编辑器。被接受，所以 onAccepted 会重置编辑态。
      if (trimmed === (entry.content ?? "").trim() && !attachmentsChanged) {
        return true;
      }
      const suppressAgentIds = triggerPreview.agents
        .filter((agent) => suppressedAgentIds.has(agent.id))
        .map((agent) => agent.id);
      try {
        await onEdit(
          entry.id,
          trimmed,
          activeIds,
          suppressAgentIds.length > 0 ? suppressAgentIds : undefined,
          initialContentBase,
        );
        setRevisionConflict(false);
        return true;
      } catch (err) {
        if (errorCode(err) === "revision_conflict") {
          setRevisionConflict(true);
        }
        toast.error(
          errorCode(err) === "revision_conflict"
            ? t(($) => $.revision.conflict)
            : err instanceof Error && err.message
            ? err.message
            : t(($) => $.comment.update_failed),
        );
        return false;
      }
    },
    onAccepted: () => {
      // 成功只能消费它自己提交的那一条：保存期间的新编辑要活下来，
      // 编辑器接着留在这些新编辑的编辑态。
      const lateMd = editorRef.current?.flushPendingUpdate?.();
      if (lateMd != null) setDraft(draftKey, lateMd);
      const store = useCommentDraftStore.getState();
      const live = store.drafts[draftKey];
      const untouched = live === undefined || live === submittedEntryRef.current;
      if (!editMountedRef.current) {
        if (untouched) store.clearDraft(draftKey);
        return;
      }
      if (!untouched) return;
      resetState();
    },
  });

  return {
    editing,
    saving,
    uploading: gate.uploading,
    onUploadingChange: gate.onUploadingChange,
    uploadingLabel: tEditor(($) => $.upload.in_progress),
    editorRef,
    editorAttachments,
    handleUpload,
    uploads,
    removeUpload,
    isDragOver,
    dropZoneProps,
    triggerPreview,
    content,
    suppressedAgentIds,
    toggleSuppressedAgent,
    draftKey,
    setDraft,
    setContent,
    clearDraft,
    initialValue,
    standaloneEditAttachments,
    retainedStandaloneIds,
    setRetainedStandaloneIds,
    startEdit,
    cancelEdit,
    saveEdit,
    revisionConflict,
    adoptServerVersion,
  };
}

function CommentRevisionConflict({
  serverContent,
  localContent,
  onKeepLocal,
  onUseServer,
  saving,
}: {
  serverContent: string;
  localContent: string;
  onKeepLocal: () => void;
  onUseServer: () => void;
  saving?: boolean;
}) {
  const { t } = useT("issues");
  return (
    <RevisionConflictCompare
      className="mt-2"
      title={t(($) => $.revision.compare_comment)}
      serverLabel={t(($) => $.revision.server_version)}
      localLabel={t(($) => $.revision.local_version)}
      serverValue={serverContent}
      localValue={localContent}
      serverAction={(
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={onUseServer}
        >
          {t(($) => $.revision.use_server)}
        </Button>
      )}
      localAction={(
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={saving}
          onClick={onKeepLocal}
        >
          {t(($) => $.revision.keep_local)}
        </Button>
      )}
    />
  );
}

// ---------------------------------------------------------------------------
// 单条评论行（同卡内的父评论与回复共用）
// ---------------------------------------------------------------------------

function CommentRow({
  issueId,
  entry,
  currentUserId,
  canModerate = false,
  isResolution = false,
  isHighlighted = false,
  onEdit,
  onDelete,
  onToggleReaction,
  onCreateSubIssue,
  onResolveToggle,
  runDiffCommentIds,
}: {
  issueId: string;
  entry: TimelineEntry;
  currentUserId?: string;
  canModerate?: boolean;
  /** 此回复是否为该线程的解决者（显示绿色徽标）。 */
  isResolution?: boolean;
  /** 此行是否为当前深链目标（正在做高亮）。 */
  isHighlighted?: boolean;
  onEdit: (commentId: string, content: string, attachmentIds: string[], suppressAgentIds?: string[], contentBase?: string) => Promise<void>;
  onDelete: (commentId: string) => void;
  onToggleReaction: (commentId: string, emoji: string) => void;
  onCreateSubIssue?: (commentId: string) => void;
  onResolveToggle?: (commentId: string, resolved: boolean) => void;
  /** See `CommentCardProps.runDiffCommentIds` — replies can be a run's newest comment. */
  runDiffCommentIds?: ReadonlyMap<string, string>;
}) {
  const { t } = useT("issues");
  const locale = useLocale();
  const timeAgo = useTimeAgo();
  const { getActorName } = useActorName();

  const edit = useEditAttachmentState(issueId, entry, onEdit);

  const isOwn = entry.actor_type === "member" && entry.actor_id === currentUserId;
  const canEditEntry = isOwn || (canModerate && entry.actor_type === "member");
  const canDeleteEntry = isOwn || canModerate;
  const [confirmDelete, setConfirmDelete] = useState(false);

  const reactions = entry.reactions ?? [];
  // 2. Replies are comments too: a run whose newest comment is a reply shows its card here.
  const runDiffTaskId =
    entry.source_task_id && runDiffCommentIds?.get(entry.source_task_id) === entry.id
      ? entry.source_task_id
      : null;

  return (
    <div className="py-1.5">
      {/* 表头钉在这条回复自己的行盒里、相对时间线的滚动父级吸顶，因此长回复滚动
          正文时作者与操作仍可见；一旦滚过这条回复的结尾就释放。表头保持不透明，
          并贴合本评论的高亮状态，同时遮挡住身下滚动的正文。 */}
      <StickyHeaderShell
        highlighted={isHighlighted}
        className="flex items-center gap-2.5 px-4 max-md:px-3 pt-1 pb-1.5"
      >
        <ActorAvatar actorType={entry.actor_type} actorId={entry.actor_id} size="md" enableHoverCard showStatusDot />
        <span className="cursor-pointer text-body font-medium">
          {getActorName(entry.actor_type, entry.actor_id)}
        </span>
        <Tooltip>
          <TooltipTrigger
            render={
              <span className="text-caption text-muted-foreground cursor-default">
                {timeAgo(entry.created_at)}
              </span>
            }
          />
          <TooltipContent side="top">
            {new Date(entry.created_at).toLocaleString(locale)}
          </TooltipContent>
        </Tooltip>

        {isResolution && (
          <span className="text-caption font-medium text-success">
            {t(($) => $.comment.resolve.resolution_badge)}
          </span>
        )}

        <div className="ml-auto flex items-center gap-0.5">
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="text-muted-foreground"
                  aria-label={t(($) => $.comment.more_actions)}
                >
                  <MoreHorizontal className="h-4 w-4" aria-hidden />
                </Button>
              }
            />
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => {
                void copyText(entry.content ?? "").then((ok) => {
                  if (ok) toast.success(t(($) => $.comment.copied_toast));
                });
              }}>
                <Copy className="h-3.5 w-3.5" />
                {t(($) => $.comment.copy_action)}
              </DropdownMenuItem>
              {onCreateSubIssue && entry.comment_type === "comment" && (
                <DropdownMenuItem onClick={() => onCreateSubIssue(entry.id)}>
                  <MessageSquarePlus className="h-3.5 w-3.5" aria-hidden />
                  {t(($) => $.source_context.create_action)}
                </DropdownMenuItem>
              )}
              {onResolveToggle && (
                <>
                  <DropdownMenuSeparator />
                  {isResolution ? (
                    <DropdownMenuItem onClick={() => onResolveToggle(entry.id, false)}>
                      <RotateCcw className="h-3.5 w-3.5" />
                      {t(($) => $.comment.resolve.unresolve_action)}
                    </DropdownMenuItem>
                  ) : (
                    <DropdownMenuItem onClick={() => onResolveToggle(entry.id, true)}>
                      <CheckCircle2 className="h-3.5 w-3.5" />
                      {t(($) => $.comment.resolve.resolve_with_comment_action)}
                    </DropdownMenuItem>
                  )}
                </>
              )}
              {(canEditEntry || canDeleteEntry) && (
                <>
                  <DropdownMenuSeparator />
                  {canEditEntry && (
                    <DropdownMenuItem onClick={edit.startEdit}>
                      <Pencil className="h-3.5 w-3.5" />
                      {t(($) => $.comment.edit_action)}
                    </DropdownMenuItem>
                  )}
                  {canEditEntry && canDeleteEntry && <DropdownMenuSeparator />}
                  {canDeleteEntry && (
                    <DropdownMenuItem onClick={() => setConfirmDelete(true)} variant="destructive">
                      <Trash2 className="h-3.5 w-3.5" />
                      {t(($) => $.comment.delete_action)}
                    </DropdownMenuItem>
                  )}
                </>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
          <DeleteCommentDialog
            open={confirmDelete}
            onOpenChange={setConfirmDelete}
            onConfirm={() => onDelete(entry.id)}
          />
        </div>
      </StickyHeaderShell>

      {edit.editing ? (
        <div
          {...edit.dropZoneProps}
          className="relative pl-12 pr-4 max-md:pl-3 max-md:pr-3 pt-1"
          onKeyDown={(e) => { if (e.key === "Escape") edit.cancelEdit(); }}
        >
          <div className="text-body leading-relaxed">
            <ContentEditor
              ref={edit.editorRef}
              defaultValue={edit.initialValue}
              placeholder={t(($) => $.comment.edit_placeholder)}
              onUpdate={(md) => {
                edit.setContent(md);
                if (md.trim().length > 0) edit.setDraft(edit.draftKey, md);
                else edit.clearDraft(edit.draftKey);
              }}
              onSubmit={edit.saveEdit}
              onUploadFile={edit.handleUpload}
              onUploadingChange={edit.onUploadingChange}
              debounceMs={100}
              currentIssueId={issueId}
              attachments={edit.editorAttachments}
            />
          </div>
          {edit.revisionConflict ? (
            <CommentRevisionConflict
              serverContent={entry.content ?? ""}
              localContent={edit.content}
              onKeepLocal={() => void edit.saveEdit()}
              onUseServer={edit.adoptServerVersion}
              saving={edit.saving}
            />
          ) : null}
          {edit.standaloneEditAttachments.length > 0 && (
            <AttachmentList
              attachments={edit.standaloneEditAttachments}
              className="mt-2 max-w-full"
              onRemove={(attachmentId) =>
                edit.setRetainedStandaloneIds((ids) => {
                  const next = new Set(ids ?? []);
                  next.delete(attachmentId);
                  return next;
                })
              }
            />
          )}
          <div className="flex items-center justify-between gap-2 mt-2">
            <div className="min-w-0 flex-1">
              <CommentTriggerChips
                agents={edit.triggerPreview.agents}
                blocked={edit.triggerPreview.blocked}
                draftContent={edit.content}
                suppressedAgentIds={edit.suppressedAgentIds}
                onToggle={edit.toggleSuppressedAgent}
              />
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <FileUploadButton
                size="sm"
                multiple
                onSelect={(file) => edit.editorRef.current?.uploadFile(file)}
              />
              <Button size="sm" variant="ghost" onClick={edit.cancelEdit} disabled={edit.saving}>{t(($) => $.comment.cancel_edit)}</Button>
              <Button
                size="sm"
                variant="outline"
                onClick={edit.saveEdit}
                disabled={edit.saving || edit.uploading}
                aria-disabled={edit.uploading || undefined}
                aria-busy={edit.uploading || undefined}
              >
                {edit.saving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                {edit.uploading ? edit.uploadingLabel : t(($) => $.comment.save_action)}
              </Button>
            </div>
          </div>
          {edit.isDragOver && <FileDropOverlay />}
        </div>
      ) : (
        <>
          <div className="pl-12 pr-4 max-md:pl-3 max-md:pr-3 pt-1 text-body leading-relaxed text-foreground">
            <ReadonlyContent content={entry.content ?? ""} attachments={entry.attachments} />
          </div>
          <AttachmentList attachments={entry.attachments} content={entry.content} className="mt-1.5 pl-12 pr-4 max-md:pl-3 max-md:pr-3" />
          {runDiffTaskId && (
            <RunDiffCard
              issueId={issueId}
              taskId={runDiffTaskId}
              className="mt-2 pl-12 pr-4 max-md:pl-3 max-md:pr-3"
            />
          )}
          {retryableAgentFailureComment(entry) && (
            <TaskCommentRetryButton
              issueId={issueId}
              taskId={entry.source_task_id}
              className="mt-2 pl-12 pr-4 max-md:pl-3 max-md:pr-3"
            />
          )}
          <ReactionBar
            reactions={reactions}
            currentUserId={currentUserId}
            onToggle={(emoji) => onToggleReaction(entry.id, emoji)}
            getActorName={getActorName}
            className="mt-1.5 pl-12 pr-4 max-md:pl-3 max-md:pr-3"
          />
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// CommentCard——每条线程一张卡片（父评论 + 全部扁平回复都在里面）
// ---------------------------------------------------------------------------

// 快捷操作会发布一条普通评论，也按普通评论渲染 (MUL-5465)。
// 它曾短暂用过"折叠的一行表头、点开才见 prompt"，理由是重复运行会把讨论埋没。
// 实际上 prompt 都很短、表头又把正文已说的话复述了一遍，这个"展开"只是在读者与
// 文本之间多加了次点击。来源信息仍挂在 `quick_action_id` 上；它是数据，不是装饰。

function CommentCardImpl({
  issueId,
  entry,
  replies,
  currentUserId,
  canModerate = false,
  onReply,
  onReplyAccepted,
  onEdit,
  onDelete,
  onToggleReaction,
  onCreateSubIssue,
  onResolveToggle,
  onCollapseResolved,
  expandedResolvedIds,
  onResolvedExpandChange,
  highlightedCommentId,
  runDiffCommentIds,
}: CommentCardProps) {
  const { t } = useT("issues");
  const locale = useLocale();
  const timeAgo = useTimeAgo();
  const { getActorName } = useActorName();
  const isCollapsed = useCommentCollapseStore((s) => s.isCollapsed(issueId, entry.id));
  const toggleCollapse = useCommentCollapseStore((s) => s.toggle);
  const open = !isCollapsed;
  const handleToggle = useCallback(
    () => toggleCollapse(issueId, entry.id),
    [toggleCollapse, issueId, entry.id],
  );

  const edit = useEditAttachmentState(issueId, entry, onEdit);

  const isOwn = entry.actor_type === "member" && entry.actor_id === currentUserId;
  const canEditEntry = isOwn || (canModerate && entry.actor_type === "member");
  const canDeleteEntry = isOwn || canModerate;
  const [confirmDelete, setConfirmDelete] = useState(false);

  const allNestedReplies = replies;

  const replyCount = allNestedReplies.length;
  const contentPreview = (entry.content ?? "").replace(/\n/g, " ").slice(0, 80);
  const reactions = entry.reactions ?? [];

  const isHighlighted = highlightedCommentId === entry.id;

  // 3. Same rule as the reply rows: only the run's newest comment carries the card.
  const runDiffTaskId =
    entry.source_task_id && runDiffCommentIds?.get(entry.source_task_id) === entry.id
      ? entry.source_task_id
      : null;

  // 回复即解决的展示。当*某条回复*是线程的解决者时，其余回复折到一根横条后面、
  // 解决者保持可见（根解决者在 issue-detail 的 resolved-bar 层处理，所以这里的
  // "root" kind 渲染普通完整线程 + 折叠表头）。
  const resolution = deriveThreadResolution(entry, allNestedReplies);
  const replyResolutionId = resolution.kind === "reply" ? resolution.resolutionId : null;
  const threadExpanded = !!expandedResolvedIds?.has(entry.id);
  const replyFolded = replyResolutionId != null && !threadExpanded;
  const foldedReplies = replyResolutionId
    ? allNestedReplies.filter((r) => r.id !== replyResolutionId)
    : allNestedReplies;
  const resolutionReply = replyResolutionId
    ? allNestedReplies.find((r) => r.id === replyResolutionId) ?? null
    : null;

  // 线程展开时，把根评论的表头钉到时间线的滚动父级，这样滚动长根评论正文时作者与
  // 操作仍可见（Card 上的 overflow-clip 把这点锚定到时间线而非卡片——见下文）。
  // 下方 root-section 容器把包含块限定到"表头 + 正文"，所以一进回复区就释放——
  // 任一时刻恰好只钉一个表头。每条回复以同样的方式把表头钉在自己的行内（见
  // CommentRow）。当解决折叠横条已经占据 top-0 吸顶位时，跳过根表头（根已解决且
  // 展开、或回复解决已展开）：同一偏移上的两条吸顶条会叠起来、遮住彼此。
  const stickyHeader =
    open && !onCollapseResolved && !(replyResolutionId != null && threadExpanded);

  return (
    // overflow-clip（而非 -hidden）裁剪圆角却不会造出滚动容器，因此下方的吸顶
    // 折叠控件能解析到时间线的滚动父级，而不是这张卡片。见 PR #3623。
    <Card className="!py-0 !gap-0 overflow-clip transition-colors duration-700">
      {onCollapseResolved && (
        <button
          type="button"
          onClick={onCollapseResolved}
          className="sticky top-0 z-20 flex w-full items-center gap-2.5 border-b border-border/50 bg-muted px-4 max-md:px-3 py-2.5 text-left text-body text-muted-foreground transition-colors cursor-pointer hover:bg-accent hover:text-accent-foreground"
          aria-label={t(($) => $.comment.resolve.collapse)}
        >
          <ListChevronsDownUp className="h-3.5 w-3.5" />
          {t(($) => $.comment.resolve.collapse)}
        </button>
      )}
      {/* root-section——吸顶表头的包含块。它只包"表头 + 根正文"，因此一旦滚过正文
            进入回复（回复渲染在容器之外），表头就释放。这正是一刻只钉一个表头的关键：
            没有这个容器，表头的包含块就是整条线程，它会一直卡在每条回复身后。 */}
        <div className={cn("transition-colors duration-700", isHighlighted && highlightedCommentBackgroundClass)}>
          {/* 表头——始终可见，兼作整卡的展开/折叠开关 */}
          <StickyHeaderShell
            sticky={stickyHeader}
            highlighted={isHighlighted}
            className="px-4 max-md:px-3 py-3"
          >
            <div className="flex items-center gap-2.5">
              <button
                type="button"
                aria-expanded={open}
                aria-controls={`comment-body-${entry.id}`}
                onClick={handleToggle}
                className="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
              >
                <ChevronRight className={cn("h-3.5 w-3.5 transition-transform", open && "rotate-90")} />
              </button>
              <ActorAvatar actorType={entry.actor_type} actorId={entry.actor_id} size="md" enableHoverCard showStatusDot />
              <span className="shrink-0 cursor-pointer text-body font-medium">
                {getActorName(entry.actor_type, entry.actor_id)}
              </span>
              <Tooltip>
                <TooltipTrigger
                  render={
                    <span className="shrink-0 text-caption text-muted-foreground cursor-default">
                      {timeAgo(entry.created_at)}
                    </span>
                  }
                />
                <TooltipContent side="top">
                  {new Date(entry.created_at).toLocaleString(locale)}
                </TooltipContent>
              </Tooltip>

              {!open && contentPreview && (
                <span className="min-w-0 flex-1 truncate text-caption text-muted-foreground">
                  {contentPreview}
                </span>
              )}
              {!open && replyCount > 0 && (
                <span className="shrink-0 text-caption text-muted-foreground">
                  {t(($) => $.comment.reply_count, { count: replyCount })}
                </span>
              )}

              {open && (
                <div className="ml-auto flex items-center gap-0.5">
                  <DropdownMenu>
                    <DropdownMenuTrigger
                      render={
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          className="text-muted-foreground"
                          aria-label={t(($) => $.comment.more_actions)}
                        >
                          <MoreHorizontal className="h-4 w-4" aria-hidden />
                        </Button>
                      }
                    />
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onClick={() => {
                        void copyText(entry.content ?? "").then((ok) => {
                          if (ok) toast.success(t(($) => $.comment.copied_toast));
                        });
                      }}>
                        <Copy className="h-3.5 w-3.5" />
                        {t(($) => $.comment.copy_action)}
                      </DropdownMenuItem>
                      {onCreateSubIssue && entry.comment_type === "comment" && (
                        <DropdownMenuItem onClick={() => onCreateSubIssue(entry.id)}>
                          <MessageSquarePlus className="h-3.5 w-3.5" aria-hidden />
                          {t(($) => $.source_context.create_action)}
                        </DropdownMenuItem>
                      )}
                      {onResolveToggle && (
                        <>
                          <DropdownMenuSeparator />
                          <DropdownMenuItem onClick={() => onResolveToggle(entry.id, !entry.resolved_at)}>
                            {entry.resolved_at ? (
                              <>
                                <RotateCcw className="h-3.5 w-3.5" />
                                {t(($) => $.comment.resolve.unresolve_thread_action)}
                              </>
                            ) : (
                              <>
                                <CheckCircle2 className="h-3.5 w-3.5" />
                                {t(($) => $.comment.resolve.resolve_thread_action)}
                              </>
                            )}
                          </DropdownMenuItem>
                        </>
                      )}
                      {(canEditEntry || canDeleteEntry) && (
                        <>
                          <DropdownMenuSeparator />
                          {canEditEntry && (
                            <DropdownMenuItem onClick={edit.startEdit}>
                              <Pencil className="h-3.5 w-3.5" />
                              {t(($) => $.comment.edit_action)}
                            </DropdownMenuItem>
                          )}
                          {canEditEntry && canDeleteEntry && <DropdownMenuSeparator />}
                          {canDeleteEntry && (
                            <DropdownMenuItem onClick={() => setConfirmDelete(true)} variant="destructive">
                              <Trash2 className="h-3.5 w-3.5" />
                              {t(($) => $.comment.delete_action)}
                            </DropdownMenuItem>
                          )}
                        </>
                      )}
                    </DropdownMenuContent>
                  </DropdownMenu>
                  <DeleteCommentDialog
                    open={confirmDelete}
                    onOpenChange={setConfirmDelete}
                    onConfirm={() => onDelete(entry.id)}
                    hasReplies
                  />
                </div>
              )}
            </div>
          </StickyHeaderShell>

        {/* 根评论正文。这里避免用 Base UI 的 Panel：每个挂载的 Panel 都会探测计算
            样式以检测动画，迫使超长 issue 详情文档里发生样式重算。 */}
        {open && (
          <div id={`comment-body-${entry.id}`} className="px-4 max-md:px-3 pt-1 pb-3">
            {edit.editing ? (
              <div
                {...edit.dropZoneProps}
                className="relative pl-10 max-md:pl-0"
                onKeyDown={(e) => { if (e.key === "Escape") edit.cancelEdit(); }}
              >
                <div className="text-body leading-relaxed">
                  <ContentEditor
                    ref={edit.editorRef}
                    defaultValue={edit.initialValue}
                    placeholder={t(($) => $.comment.edit_placeholder)}
                    onUpdate={(md) => {
                      edit.setContent(md);
                      if (md.trim().length > 0) edit.setDraft(edit.draftKey, md);
                      else edit.clearDraft(edit.draftKey);
                    }}
                    onSubmit={edit.saveEdit}
                    onUploadFile={edit.handleUpload}
                    onUploadingChange={edit.onUploadingChange}
                    debounceMs={100}
                    currentIssueId={issueId}
                    attachments={edit.editorAttachments}
                  />
                </div>
                {edit.revisionConflict ? (
                  <CommentRevisionConflict
                    serverContent={entry.content ?? ""}
                    localContent={edit.content}
                    onKeepLocal={() => void edit.saveEdit()}
                    onUseServer={edit.adoptServerVersion}
                    saving={edit.saving}
                  />
                ) : null}
                <div className="flex items-center justify-between mt-2">
                  <div className="flex min-w-0 flex-1 flex-col gap-1">
                    {edit.standaloneEditAttachments.length > 0 && (
                      <AttachmentList
                        attachments={edit.standaloneEditAttachments}
                        className="max-w-full"
                        onRemove={(attachmentId) =>
                          edit.setRetainedStandaloneIds((ids) => {
                            const next = new Set(ids ?? []);
                            next.delete(attachmentId);
                            return next;
                          })
                        }
                        />
                      )}
                    <CommentTriggerChips
                      agents={edit.triggerPreview.agents}
                      blocked={edit.triggerPreview.blocked}
                      draftContent={edit.content}
                      suppressedAgentIds={edit.suppressedAgentIds}
                      onToggle={edit.toggleSuppressedAgent}
                    />
                    <FileUploadButton
                      size="sm"
                      multiple
                      onSelect={(file) => edit.editorRef.current?.uploadFile(file)}
                    />
                  </div>
                  <div className="flex items-center gap-2">
                    <Button size="sm" variant="ghost" onClick={edit.cancelEdit} disabled={edit.saving}>{t(($) => $.comment.cancel_edit)}</Button>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={edit.saveEdit}
                      disabled={edit.saving || edit.uploading}
                      aria-disabled={edit.uploading || undefined}
                      aria-busy={edit.uploading || undefined}
                    >
                      {edit.saving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                      {edit.uploading ? edit.uploadingLabel : t(($) => $.comment.save_action)}
                    </Button>
                  </div>
                </div>
                {edit.isDragOver && <FileDropOverlay />}
              </div>
            ) : (
              <>
                <div className="pl-10 max-md:pl-0 text-body leading-relaxed text-foreground">
                  <ReadonlyContent content={entry.content ?? ""} attachments={entry.attachments} />
                </div>
                <AttachmentList attachments={entry.attachments} content={entry.content} className="mt-1.5 pl-10 max-md:pl-0" />
                {runDiffTaskId && (
                  <RunDiffCard
                    issueId={issueId}
                    taskId={runDiffTaskId}
                    className="mt-2 pl-10 max-md:pl-0"
                  />
                )}
                {retryableAgentFailureComment(entry) && (
                  <TaskCommentRetryButton
                    issueId={issueId}
                    taskId={entry.source_task_id}
                    className="mt-2 pl-10 max-md:pl-0"
                  />
                )}
                <ReactionBar
                  reactions={reactions}
                  currentUserId={currentUserId}
                  onToggle={(emoji) => onToggleReaction(entry.id, emoji)}
                  getActorName={getActorName}
                  className="mt-1.5 pl-10 max-md:pl-0"
                />
              </>
            )}
          </div>
        )}
        </div>

        {/* 回复 + 回复输入框——渲染在 root-section 之外，让根表头的吸顶包含块止步于
            正文。随 `open` 门控，与正文 Panel 的折叠可见性一致。 */}
        {open && (
          <>
          {replyFolded ? (
            <>
              {/* 回复解决折叠态：其余回复藏在横条后，解决者钉在下方 */}
              {foldedReplies.length > 0 && (
                <div className="border-t border-border/50 px-4 max-md:px-3 py-2.5">
                  <CommentsFoldBar
                    replies={foldedReplies}
                    onExpand={() => onResolvedExpandChange?.(entry.id, true)}
                  />
                </div>
              )}
              {resolutionReply && (
                <div
                  id={`comment-${resolutionReply.id}`}
                  className={cn(
                    "border-t border-border/50 transition-colors duration-700",
                    highlightedCommentId === resolutionReply.id && highlightedCommentBackgroundClass,
                  )}
                >
                  <CommentRow
                    issueId={issueId}
                    entry={resolutionReply}
                    currentUserId={currentUserId}
                    canModerate={canModerate}
                    isResolution
                    isHighlighted={highlightedCommentId === resolutionReply.id}
                    onEdit={onEdit}
                    onDelete={onDelete}
                    onToggleReaction={onToggleReaction}
                    onCreateSubIssue={onCreateSubIssue}
                    onResolveToggle={onResolveToggle}
                    runDiffCommentIds={runDiffCommentIds}
                  />
                </div>
              )}
            </>
          ) : (
            <>
              {/* 回复解决展开态：提供"折叠回去"入口 */}
              {replyResolutionId != null && onResolvedExpandChange && (
                <button
                  type="button"
                  onClick={() => onResolvedExpandChange(entry.id, false)}
                  className="sticky top-0 z-20 flex w-full items-center gap-2.5 border-t border-border/50 bg-muted px-4 max-md:px-3 py-2.5 text-left text-body text-muted-foreground transition-colors cursor-pointer hover:bg-accent hover:text-accent-foreground"
                  aria-label={t(($) => $.comment.resolve.collapse)}
                >
                  <ListChevronsDownUp className="h-3.5 w-3.5" />
                  {t(($) => $.comment.resolve.collapse)}
                </button>
              )}
              {/* 回复——按时间排序；解决者留在原位并带徽标 */}
              {allNestedReplies.map((reply) => (
                <div
                  key={reply.id}
                  id={`comment-${reply.id}`}
                  className={cn(
                    "border-t border-border/50 transition-colors duration-700",
                    highlightedCommentId === reply.id && highlightedCommentBackgroundClass,
                  )}
                >
                  <CommentRow
                    issueId={issueId}
                    entry={reply}
                    currentUserId={currentUserId}
                    canModerate={canModerate}
                    isResolution={reply.id === replyResolutionId}
                    isHighlighted={highlightedCommentId === reply.id}
                    onEdit={onEdit}
                    onDelete={onDelete}
                    onToggleReaction={onToggleReaction}
                    onCreateSubIssue={onCreateSubIssue}
                    onResolveToggle={onResolveToggle}
                    runDiffCommentIds={runDiffCommentIds}
                  />
                </div>
              ))}

              {/* 回复输入框 */}
              <div className="border-t border-border/50 px-4 max-md:px-3 py-2.5">
                <ReplyInput
                  issueId={issueId}
                  parentId={entry.id}
                  placeholder={t(($) => $.reply.placeholder)}
                  size="sm"
                  avatarType="member"
                  avatarId={currentUserId ?? ""}
                  draftKey={`reply:${issueId}:${entry.id}`}
                  onSubmit={(content, attachmentIds, suppressAgentIds) => onReply(entry.id, content, attachmentIds, suppressAgentIds)}
                  onAccepted={onReplyAccepted}
                />
              </div>
            </>
          )}
          </>
        )}
    </Card>
  );
}

// 已 memo 化：让超长时间线（例如收件箱内嵌的、有几千条评论的 IssueDetail）不会因
// 每次父状态更新或 WS 驱动的缓存刷新而重渲染每一张卡。默认浅比较就够：时间线分组
// 在 issue-detail.tsx 里 useMemo 过（Map 引用稳定），每个回调也都经
// use-issue-timeline.ts 的 useCallback 稳定化。
const CommentCard = memo(CommentCardImpl);

export { CommentCard, type CommentCardProps };
