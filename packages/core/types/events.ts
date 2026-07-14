import type { Issue, IssueMetadata, IssueReaction } from "./issue";
import type { InboxItem } from "./inbox";
import type { Comment, Reaction } from "./comment";
import type { TimelineEntry } from "./activity";
import type { Workspace, MemberWithUser } from "./workspace";
import type { Label } from "./label";

// WebSocket event types (matching Go server protocol/events.go)
export type WSEventType =
  | "issue:created"
  | "issue:updated"
  | "issue:deleted"
  | "comment:created"
  | "comment:updated"
  | "comment:deleted"
  | "comment:resolved"
  | "comment:unresolved"
  | "agent:status"
  | "agent:created"
  | "agent:archived"
  | "agent:restored"
  | "task:queued"
  | "task:dispatch"
  | "task:running"
  | "task:progress"
  | "task:completed"
  | "task:failed"
  | "task:message"
  | "task:cancelled"
  | "inbox:new"
  | "inbox:read"
  | "inbox:archived"
  | "inbox:batch-read"
  | "inbox:batch-archived"
  | "workspace:updated"
  | "workspace:deleted"
  | "member:added"
  | "member:updated"
  | "member:removed"
  | "daemon:heartbeat"
  | "daemon:register"
  | "skill:created"
  | "skill:updated"
  | "skill:deleted"
  | "subscriber:added"
  | "subscriber:removed"
  | "activity:created"
  | "reaction:added"
  | "reaction:removed"
  | "issue_reaction:added"
  | "issue_reaction:removed"
  | "chat:message"
  | "chat:done"
  | "chat:session_read"
  | "chat:session_deleted"
  | "chat:session_updated"
  | "project:created"
  | "project:updated"
  | "project:deleted"
  | "project_resource:created"
  | "project_resource:updated"
  | "project_resource:deleted"
  | "squad:created"
  | "squad:updated"
  | "squad:deleted"
  | "squad:restored"
  | "label:created"
  | "label:updated"
  | "label:deleted"
  | "issue_labels:changed"
  | "issue_metadata:changed"
  | "pin:created"
  | "pin:deleted"
  | "pin:reordered"
  | "github_installation:created"
  | "github_installation:deleted"
  | "pull_request:linked"
  | "pull_request:updated";

export interface WSMessage<T = unknown> {
  type: WSEventType;
  payload: T;
  actor_id?: string;
  actor_type?: string;
}

export interface IssueCreatedPayload {
  issue: Issue;
}

export interface IssueUpdatedPayload {
  issue: Issue;
}

export interface IssueDeletedPayload {
  issue_id: string;
}

export interface IssueLabelsChangedPayload {
  issue_id: string;
  labels: Label[];
}

export interface IssueMetadataChangedPayload {
  issue_id: string;
  metadata: IssueMetadata;
}

export interface InboxNewPayload {
  item: InboxItem;
}

export interface CommentCreatedPayload {
  comment: Comment;
}

export interface CommentUpdatedPayload {
  comment: Comment;
}

export interface CommentDeletedPayload {
  comment_id: string;
  issue_id: string;
}

export interface CommentResolvedPayload {
  comment: Comment;
}

export interface CommentUnresolvedPayload {
  comment: Comment;
}

export interface WorkspaceUpdatedPayload {
  workspace: Workspace;
}

export interface WorkspaceDeletedPayload {
  workspace_id: string;
}

export interface MemberAddedPayload {
  member: MemberWithUser;
  workspace_id: string;
  workspace_name?: string;
}

export interface MemberRemovedPayload {
  member_id: string;
  user_id: string;
  workspace_id: string;
}

export interface SubscriberAddedPayload {
  issue_id: string;
  user_type: string;
  user_id: string;
  reason: string;
}

export interface SubscriberRemovedPayload {
  issue_id: string;
  user_type: string;
  user_id: string;
}

export interface ActivityCreatedPayload {
  issue_id: string;
  entry: TimelineEntry;
}

export interface TaskMessagePayload {
  task_id: string;
  issue_id: string;
  seq: number;
  type: "text" | "thinking" | "tool_use" | "tool_result" | "error";
  tool?: string;
  content?: string;
  input?: Record<string, unknown>;
  output?: string;
  created_at?: string;
}

export interface TaskLifecyclePayload {
  task_id: string;
  issue_id: string;
  chat_session_id?: string;
}

export interface ReactionAddedPayload {
  reaction: Reaction;
  issue_id: string;
}

export interface ReactionRemovedPayload {
  comment_id: string;
  issue_id: string;
  emoji: string;
  actor_type: string;
  actor_id: string;
}

export interface IssueReactionAddedPayload {
  reaction: IssueReaction;
  issue_id: string;
}

export interface IssueReactionRemovedPayload {
  issue_id: string;
  emoji: string;
  actor_type: string;
  actor_id: string;
}

export interface ChatDonePayload {
  chat_session_id: string;
  task_id: string;
  /**
   * Server populates these from the freshly-persisted assistant ChatMessage
   * row so the WS handler can write it into the messages cache inline. An
   * empty completion creates no assistant row, so these fields stay optional.
   */
  message_id?: string;
  content?: string;
  elapsed_ms?: number;
}
