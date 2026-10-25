"use client";

// 问题时间线 hook：管理一个问题详情页的完整时间线（评论 + 活动），
// 提供发送/回复/编辑/删除评论、解决/取消解决、切换表情反应等能力，
// 并通过 WS 实时把别人的评论、活动、反应增量合进 React Query 缓存。
// 本文件直接使用服务端时间线缓存（单一事实来源），所以删除评论会连带删掉它的回复子树。
import { useEffect, useRef, useCallback, useMemo } from "react";
import {
  useQuery,
  useQueryClient,
  useMutationState,
} from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import type {
  Comment,
  TimelineEntry,
  Reaction,
} from "@multica/core/types";
import type {
  CommentCreatedPayload,
  CommentUpdatedPayload,
  CommentDeletedPayload,
  CommentResolvedPayload,
  CommentUnresolvedPayload,
  ActivityCreatedPayload,
  ReactionAddedPayload,
  ReactionRemovedPayload,
} from "@multica/core/types";
import {
  issueTimelineOptions,
  issueKeys,
} from "@multica/core/issues/queries";
import {
  useCreateComment,
  useUpdateComment,
  useDeleteComment,
  useResolveComment,
  useToggleCommentReaction,
  type ToggleCommentReactionVars,
} from "@multica/core/issues/mutations";
import { sortTimelineEntriesAsc } from "@multica/core/issues/timeline-sort";
import {
  unhandledCommentTriggerOutcomes,
  mentionLabelsByTarget,
  parseReviewReply,
} from "@multica/core/issues/comment-trigger-outcomes";
import { useWSEvent, useWSReconnect } from "@multica/core/realtime";
import { removeCommentSubtree } from "@multica/core/issues/comment-deletion";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { blockedShortReasonLabel } from "../blocked-trigger-copy";
import { reviewReplyToast } from "../review-reply-copy";

type TLCache = TimelineEntry[];

function commentToTimelineEntry(c: Comment): TimelineEntry {
  return {
    type: "comment",
    id: c.id,
    actor_type: c.author_type,
    actor_id: c.author_id,
    content: c.content,
    parent_id: c.parent_id,
    created_at: c.created_at,
    updated_at: c.updated_at,
    revision: c.revision,
    comment_type: c.type,
    reactions: c.reactions ?? [],
    attachments: c.attachments ?? [],
    resolved_at: c.resolved_at,
    resolved_by_type: c.resolved_by_type,
    resolved_by_id: c.resolved_by_id,
    source_task_id: c.source_task_id,
    deleted_at: c.deleted_at,
  };
}

function acceptsCommentRevision(
  current: TimelineEntry,
  incoming: Comment,
): boolean {
  return (
    current.revision === undefined ||
    (incoming.revision !== undefined && incoming.revision >= current.revision)
  );
}

function applyCommentSnapshot(
  qc: QueryClient,
  issueId: string,
  comment: Comment,
) {
  let missingRevision = false;
  qc.setQueryData<TLCache>(issueKeys.timeline(issueId), (old) =>
    old?.map((entry) => {
      if (entry.id !== comment.id) return entry;
      if (entry.revision !== undefined && comment.revision === undefined) {
        missingRevision = true;
        return entry;
      }
      return acceptsCommentRevision(entry, comment)
        ? commentToTimelineEntry(comment)
        : entry;
    }),
  );
  if (missingRevision) {
    // 滚动发布期间，老版本服务端可能在本缓存已观察到带版本号的快照之后，再发出一份
    // 不带版本号的快照。绝不能覆盖那个有序的值；改回从权威 API 重新拉取。
    qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
  }
}

/**
 * 管理某问题的时间线：加载列表，发送/回复/编辑/删除评论、解决评论、切换表情，
 * 并通过 WS 实时同步他人操作。
 * @param issueId - 问题 id。
 * @param userId - 当前用户 id（用于乐观地判断哪些反应/操作是「我」的）。
 */
export function useIssueTimeline(issueId: string, userId?: string) {
  const { t } = useT("issues");
  const qc = useQueryClient();

  const query = useQuery(issueTimelineOptions(issueId));
  const { data, isLoading: loading } = query;

  const timeline = useMemo<TimelineEntry[]>(() => data ?? [], [data]);

  // 稳定的 mutation 句柄。TanStack v5 每次渲染都会从 useMutation 返回一个新的结果
  // 包装，但内部的 mutateAsync / mutate 函数是稳定的。只取出它们，让下游的
  // useCallback 身份不会跟着父级每次重渲染而变——如果列出整个 mutation 对象，
  // 会废掉 CommentCard 上的 React.memo。
  const { mutateAsync: createComment } = useCreateComment(issueId);
  const { mutateAsync: updateComment } = useUpdateComment(issueId);
  const { mutateAsync: deleteCommentAsync } = useDeleteComment(issueId);
  const { mutateAsync: resolveCommentAsync } = useResolveComment(issueId);
  const { mutate: toggleCommentReaction } = useToggleCommentReaction(issueId);

  // 断线重连后的恢复：让查询失效，下次渲染重新拉取整条时间线。
  // 比去 diff 一次可能很长的断线要便宜。
  useWSReconnect(
    useCallback(() => {
      qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
    }, [qc, issueId]),
  );

  // --- WS 事件处理器（把其他用户的操作增量合进缓存） ---

  useWSEvent(
    "comment:created",
    useCallback(
      (payload: unknown) => {
        const { comment } = payload as CommentCreatedPayload;
        if (comment.issue_id !== issueId) return;
        qc.setQueryData<TLCache>(issueKeys.timeline(issueId), (old) => {
          const entry = commentToTimelineEntry(comment);
          if (!old) return [entry];
          if (old.some((e) => e.id === comment.id)) return old;
          return sortTimelineEntriesAsc([...old, entry]);
        });
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "comment:updated",
    useCallback(
      (payload: unknown) => {
        const { comment } = payload as CommentUpdatedPayload;
        if (comment.issue_id !== issueId) return;
        applyCommentSnapshot(qc, issueId, comment);
      },
      [qc, issueId],
    ),
  );

  // 为 comment:resolved / comment:unresolved 提供细粒度的处理器。事件载荷携带完整的
  // Comment（含新的 resolved_at/resolved_by_* 字段），`commentToTimelineEntry` 已能保留
  // 它们，所以直接就地替换现有条目即可。没有这两个处理器时，唯一更新缓存的路径是
  // `useRealtimeSync` 的全局失效——那会强制整条时间线重新拉取，打爆每个 CommentCard 的 memo。
  useWSEvent(
    "comment:resolved",
    useCallback(
      (payload: unknown) => {
        const { comment } = payload as CommentResolvedPayload;
        if (comment.issue_id !== issueId) return;
        applyCommentSnapshot(qc, issueId, comment);
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "comment:unresolved",
    useCallback(
      (payload: unknown) => {
        const { comment } = payload as CommentUnresolvedPayload;
        if (comment.issue_id !== issueId) return;
        applyCommentSnapshot(qc, issueId, comment);
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "comment:deleted",
    useCallback(
      (payload: unknown) => {
        const { comment_id, issue_id } = payload as CommentDeletedPayload;
        if (issue_id !== issueId) return;
        // A comment with replies is tombstoned (comment:updated), never
        // removed, so any cached reply of a removed comment is stale: older
        // servers cascaded the delete to every descendant.
        qc.setQueryData<TLCache>(issueKeys.timeline(issueId), (old) =>
          old ? removeCommentSubtree(old, comment_id) : old,
        );
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "activity:created",
    useCallback(
      (payload: unknown) => {
        const p = payload as ActivityCreatedPayload;
        if (p.issue_id !== issueId) return;
        const entry = p.entry;
        if (!entry || !entry.id) return;
        qc.setQueryData<TLCache>(issueKeys.timeline(issueId), (old) => {
          if (!old) return [entry];
          if (old.some((e) => e.id === entry.id)) return old;
          return sortTimelineEntriesAsc([...old, entry]);
        });
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "reaction:added",
    useCallback(
      (payload: unknown) => {
        const { reaction, issue_id, comment_revision } = payload as ReactionAddedPayload;
        if (issue_id !== issueId) return;
        let missingRevision = false;
        qc.setQueryData<TLCache>(issueKeys.timeline(issueId), (old) =>
          old?.map((e) => {
            if (e.id !== reaction.comment_id) return e;
            if (comment_revision === undefined && e.revision !== undefined) {
              missingRevision = true;
              return e;
            }
            if (
              comment_revision !== undefined &&
              e.revision !== undefined &&
              comment_revision < e.revision
            ) return e;
            const existing = e.reactions ?? [];
            if (existing.some((r) => r.id === reaction.id)) return e;
            return {
              ...e,
              revision: comment_revision ?? e.revision,
              reactions: [...existing, reaction],
            };
          }),
        );
        if (missingRevision) {
          qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
        }
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "reaction:removed",
    useCallback(
      (payload: unknown) => {
        const p = payload as ReactionRemovedPayload;
        if (p.issue_id !== issueId) return;
        let missingRevision = false;
        qc.setQueryData<TLCache>(issueKeys.timeline(issueId), (old) =>
          old?.map((e) => {
            if (e.id !== p.comment_id) return e;
            if (p.comment_revision === undefined && e.revision !== undefined) {
              missingRevision = true;
              return e;
            }
            if (
              p.comment_revision !== undefined &&
              e.revision !== undefined &&
              p.comment_revision < e.revision
            ) return e;
            return {
              ...e,
              revision: p.comment_revision ?? e.revision,
              reactions: (e.reactions ?? []).filter(
                (r) =>
                  !(
                    r.emoji === p.emoji &&
                    r.actor_type === p.actor_type &&
                    r.actor_id === p.actor_id
                  ),
              ),
            };
          }),
        );
        if (missingRevision) {
          qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
        }
      },
      [qc, issueId],
    ),
  );

  // --- 各类评论/活动的 mutation 方法 ---

  // 评论保存成功了，但某次 @ 提及没有明确触发（被阻止，或状态未知/未来态而不能假设成功）。
  // 这种情况下要警告而不是静默无反应（MUL-4525 §2）：评论确实发出去了，
  // 只是有 N 个被显式点名的目标没有被触发。
  const warnUnhandledTriggers = useCallback(
    (triggerOutcomes: unknown, content?: string) => {
      const unhandled = unhandledCommentTriggerOutcomes(triggerOutcomes);
      if (unhandled.length === 0) return;
      // 只有单次提及被拒时才点名：已发布评论的标记里带着用户敲的标签
      // （线上的 outcome 出于枚举安全省略了它）。多次被拒就回退成只报个数。
      if (unhandled.length === 1) {
        const outcome = unhandled[0]!;
        const name = mentionLabelsByTarget(content ?? "").get(
          `${outcome.target_type}:${outcome.target_id}`,
        );
        if (name) {
          toast.warning(
            t(($) => $.comment.posted_partial_trigger_named, {
              name,
              reason: blockedShortReasonLabel(outcome.reason_code, t),
            }),
          );
          return;
        }
      }
      toast.warning(
        t(($) => $.comment.posted_partial_trigger, { count: unhandled.length }),
      );
    },
    [t],
  );

  // 回复审查员时，回执必须说清这条回复实际交给了谁、运行有没有真的开始（GONG-63）：
  // 「评论保存成功」不等于「Agent 已启动」，转交失败也不能静默。
  const notifyReviewReply = useCallback(
    (raw: unknown) => {
      const reply = parseReviewReply(raw);
      if (!reply) return;
      const copy = reviewReplyToast(reply, t);
      if (!copy) return;
      if (copy.tone === "warning") toast.warning(copy.text);
      else toast.info(copy.text);
    },
    [t],
  );

  // 成功返回 true、失败返回 false。编辑器会一直保留用户的文字（编辑锁定 + 按钮转圈）
  // 直到这里落定，并且只在成功时才清空——所以一次慢发送不会再让输入框旁边留着一条
  // 已发送的评论，而一次失败的发送也会保留草稿。
  const submitComment = useCallback(
    async (content: string, attachmentIds?: string[], suppressAgentIds?: string[]): Promise<string | false> => {
      if (!content.trim() || !userId) return false;
      try {
        const comment = await createComment({ content, attachmentIds, suppressAgentIds });
        warnUnhandledTriggers(comment?.trigger_outcomes, comment?.content);
        notifyReviewReply(comment?.review_reply);
        return comment.id;
      } catch (err) {
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.comment.send_failed),
        );
        return false;
      }
    },
    [userId, createComment, warnUnhandledTriggers, notifyReviewReply, t],
  );

  const submitReply = useCallback(
    async (parentId: string, content: string, attachmentIds?: string[], suppressAgentIds?: string[]): Promise<string | false> => {
      if (!content.trim() || !userId) return false;
      try {
        const comment = await createComment({
          content,
          type: "comment",
          parentId,
          attachmentIds,
          suppressAgentIds,
        });
        warnUnhandledTriggers(comment?.trigger_outcomes, comment?.content);
        notifyReviewReply(comment?.review_reply);
        return comment.id;
      } catch (err) {
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.comment.send_reply_failed),
        );
        return false;
      }
    },
    [userId, createComment, warnUnhandledTriggers, notifyReviewReply, t],
  );

  const editComment = useCallback(
    async (commentId: string, content: string, attachmentIds: string[], suppressAgentIds?: string[], contentBase?: string) => {
      const comment = await updateComment({
        commentId,
        content,
        attachmentIds,
        suppressAgentIds,
        contentBase,
      });
      warnUnhandledTriggers(comment?.trigger_outcomes, comment?.content);
      notifyReviewReply(comment?.review_reply);
    },
    [updateComment, warnUnhandledTriggers, notifyReviewReply],
  );

  const deleteComment = useCallback(
    async (commentId: string) => {
      try {
        await deleteCommentAsync(commentId);
      } catch (err) {
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.comment.delete_failed),
        );
      }
    },
    [deleteCommentAsync, t],
  );

  const toggleResolveComment = useCallback(
    async (commentId: string, resolved: boolean) => {
      try {
        await resolveCommentAsync({ commentId, resolved });
      } catch (err) {
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : resolved
              ? t(($) => $.comment.resolve.resolve_failed)
              : t(($) => $.comment.resolve.unresolve_failed),
        );
      }
    },
    [resolveCommentAsync, t],
  );

  // --- 评论表情的乐观 UI ---
  // 在渲染时根据进行中的 mutation 参数推导，而不是往缓存里写临时数据
  // （那会跟 WS 事件竞争）。

  const pendingReactionVars = useMutationState({
    filters: {
      mutationKey: ["toggleCommentReaction", issueId],
      status: "pending",
    },
    select: (m) =>
      m.state.variables as ToggleCommentReactionVars | undefined,
  });

  const optimisticTimeline = useMemo(() => {
    if (pendingReactionVars.length === 0) return timeline;

    return timeline.map((entry) => {
      const pendingForEntry = pendingReactionVars.filter(
        (v) => v && v.commentId === entry.id,
      );
      if (pendingForEntry.length === 0) return entry;

      let reactions = entry.reactions ?? [];
      for (const vars of pendingForEntry) {
        if (!vars) continue;
        if (vars.existing) {
          reactions = reactions.filter((r) => r.id !== vars.existing!.id);
        } else {
          const alreadyExists = reactions.some(
            (r) =>
              r.emoji === vars.emoji &&
              r.actor_type === "member" &&
              r.actor_id === userId,
          );
          if (!alreadyExists) {
            reactions = [
              ...reactions,
              {
                id: `optimistic-${vars.emoji}`,
                comment_id: vars.commentId,
                actor_type: "member",
                actor_id: userId ?? "",
                emoji: vars.emoji,
                created_at: "",
              },
            ];
          }
        }
      }
      return { ...entry, reactions };
    });
  }, [timeline, pendingReactionVars, userId]);

  // toggleReaction 通过 ref 读取数据，这样它的身份不会随每次 WS 事件变化。否则下游每个
  // 被 memo 化的 CommentCard 都会在时间线每次变动时重渲染，长时间线上 React.memo
  // 省下的开销就白费了（#1968）。
  const timelineRef = useRef(timeline);
  useEffect(() => {
    timelineRef.current = timeline;
  }, [timeline]);

  const toggleReaction = useCallback(
    async (commentId: string, emoji: string) => {
      if (!userId) return;
      const entry = timelineRef.current.find((e) => e.id === commentId);
      const existing: Reaction | undefined = (entry?.reactions ?? []).find(
        (r) =>
          r.emoji === emoji &&
          r.actor_type === "member" &&
          r.actor_id === userId,
      );
      toggleCommentReaction({ commentId, emoji, existing });
    },
    [userId, toggleCommentReaction],
  );

  return {
    timeline: optimisticTimeline,
    loading,
    submitComment,
    submitReply,
    editComment,
    deleteComment,
    toggleResolveComment,
    toggleReaction,
  };
}
