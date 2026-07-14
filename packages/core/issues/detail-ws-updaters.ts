import type { QueryClient } from "@tanstack/react-query";
import type {
  ActivityCreatedPayload,
  Comment,
  CommentChangedPayload,
  CommentDeletedPayload,
  IssueReaction,
  IssueReactionAddedPayload,
  IssueReactionRemovedPayload,
  IssueSubscriber,
  ReactionAddedPayload,
  ReactionRemovedPayload,
  SubscriberChangedPayload,
  TimelineEntry,
  WSEventType,
} from "../types";
import { issueKeys } from "./queries";
import { sortTimelineEntriesAsc } from "./timeline-sort";

type TimelineCache = TimelineEntry[];

export const issueDetailEvents = [
  "comment:created",
  "comment:updated",
  "comment:deleted",
  "comment:resolved",
  "comment:unresolved",
  "activity:created",
  "reaction:added",
  "reaction:removed",
  "issue_reaction:added",
  "issue_reaction:removed",
  "subscriber:added",
  "subscriber:removed",
] as const satisfies readonly WSEventType[];

export type IssueDetailEvent = (typeof issueDetailEvents)[number];

function commentToTimelineEntry(comment: Comment): TimelineEntry {
  return {
    type: "comment",
    id: comment.id,
    actor_type: comment.author_type,
    actor_id: comment.author_id,
    content: comment.content,
    parent_id: comment.parent_id,
    created_at: comment.created_at,
    comment_type: comment.type,
    reactions: comment.reactions ?? [],
    attachments: comment.attachments ?? [],
    resolved_at: comment.resolved_at,
    resolved_by_type: comment.resolved_by_type,
    resolved_by_id: comment.resolved_by_id,
    source_task_id: comment.source_task_id,
  };
}

function updateTimeline(
  qc: QueryClient,
  issueId: string,
  update: (old: TimelineCache | undefined) => TimelineCache | undefined,
): void {
  qc.setQueryData<TimelineCache>(issueKeys.timeline(issueId), update);
  // An inactive cache is marked stale and refetches on its next mount. The
  // active cache stays instant because the event payload is applied above.
  qc.invalidateQueries({
    queryKey: issueKeys.timeline(issueId),
    refetchType: "none",
  });
}

/**
 * Applies issue-detail WebSocket events to React Query's canonical caches.
 * This is the sole owner of these event projections; view hooks only read the
 * caches and expose mutations.
 */
export function applyIssueDetailEvent(
  qc: QueryClient,
  event: IssueDetailEvent,
  payload: unknown,
): void {
  switch (event) {
    case "comment:created": {
      const { comment } = payload as CommentChangedPayload;
      if (!comment?.issue_id) return;
      updateTimeline(qc, comment.issue_id, (old) => {
        const entry = commentToTimelineEntry(comment);
        if (!old) return [entry];
        if (old.some((item) => item.id === comment.id)) return old;
        return sortTimelineEntriesAsc([...old, entry]);
      });
      return;
    }
    case "comment:updated":
    case "comment:resolved":
    case "comment:unresolved": {
      const { comment } = payload as CommentChangedPayload;
      if (!comment?.issue_id) return;
      updateTimeline(qc, comment.issue_id, (old) =>
        old?.map((item) =>
          item.id === comment.id ? commentToTimelineEntry(comment) : item,
        ),
      );
      return;
    }
    case "comment:deleted": {
      const { comment_id: commentId, issue_id: issueId } =
        payload as CommentDeletedPayload;
      if (!issueId) return;
      updateTimeline(qc, issueId, (old) => {
        if (!old) return old;
        const removed = new Set([commentId]);
        let changed = true;
        while (changed) {
          changed = false;
          for (const item of old) {
            if (
              item.parent_id &&
              removed.has(item.parent_id) &&
              !removed.has(item.id)
            ) {
              removed.add(item.id);
              changed = true;
            }
          }
        }
        return old.filter((item) => !removed.has(item.id));
      });
      return;
    }
    case "activity:created": {
      const { issue_id: issueId, entry } = payload as ActivityCreatedPayload;
      if (!issueId || !entry?.id) return;
      updateTimeline(qc, issueId, (old) => {
        if (!old) return [entry];
        if (old.some((item) => item.id === entry.id)) return old;
        return sortTimelineEntriesAsc([...old, entry]);
      });
      return;
    }
    case "reaction:added": {
      const { issue_id: issueId, reaction } = payload as ReactionAddedPayload;
      if (!issueId) return;
      updateTimeline(qc, issueId, (old) =>
        old?.map((item) => {
          if (item.id !== reaction.comment_id) return item;
          const reactions = item.reactions ?? [];
          if (reactions.some((current) => current.id === reaction.id)) return item;
          return { ...item, reactions: [...reactions, reaction] };
        }),
      );
      return;
    }
    case "reaction:removed": {
      const removed = payload as ReactionRemovedPayload;
      if (!removed.issue_id) return;
      updateTimeline(qc, removed.issue_id, (old) =>
        old?.map((item) =>
          item.id !== removed.comment_id
            ? item
            : {
                ...item,
                reactions: (item.reactions ?? []).filter(
                  (reaction) =>
                    !(
                      reaction.emoji === removed.emoji &&
                      reaction.actor_type === removed.actor_type &&
                      reaction.actor_id === removed.actor_id
                    ),
                ),
              },
        ),
      );
      return;
    }
    case "issue_reaction:added": {
      const { issue_id: issueId, reaction } =
        payload as IssueReactionAddedPayload;
      if (!issueId) return;
      qc.setQueryData<IssueReaction[]>(issueKeys.reactions(issueId), (old) => {
        if (!old || old.some((current) => current.id === reaction.id)) return old;
        return [...old, reaction];
      });
      qc.invalidateQueries({ queryKey: issueKeys.reactions(issueId) });
      return;
    }
    case "issue_reaction:removed": {
      const removed = payload as IssueReactionRemovedPayload;
      if (!removed.issue_id) return;
      qc.setQueryData<IssueReaction[]>(
        issueKeys.reactions(removed.issue_id),
        (old) =>
          old?.filter(
            (reaction) =>
              !(
                reaction.emoji === removed.emoji &&
                reaction.actor_type === removed.actor_type &&
                reaction.actor_id === removed.actor_id
              ),
          ),
      );
      qc.invalidateQueries({ queryKey: issueKeys.reactions(removed.issue_id) });
      return;
    }
    case "subscriber:added": {
      const added = payload as SubscriberChangedPayload;
      if (!added.issue_id) return;
      qc.setQueryData<IssueSubscriber[]>(
        issueKeys.subscribers(added.issue_id),
        (old) => {
          if (
            !old ||
            old.some(
              (subscriber) =>
                subscriber.user_id === added.user_id &&
                subscriber.user_type === added.user_type,
            )
          ) {
            return old;
          }
          return [
            ...old,
            {
              user_type: added.user_type as "member" | "agent",
              user_id: added.user_id,
            },
          ];
        },
      );
      qc.invalidateQueries({ queryKey: issueKeys.subscribers(added.issue_id) });
      return;
    }
    case "subscriber:removed": {
      const removed = payload as SubscriberChangedPayload;
      if (!removed.issue_id) return;
      qc.setQueryData<IssueSubscriber[]>(
        issueKeys.subscribers(removed.issue_id),
        (old) =>
          old?.filter(
            (subscriber) =>
              !(
                subscriber.user_id === removed.user_id &&
                subscriber.user_type === removed.user_type
              ),
          ),
      );
      qc.invalidateQueries({ queryKey: issueKeys.subscribers(removed.issue_id) });
      return;
    }
  }
}
