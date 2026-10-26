// 本文件定义 WebSocket 实时事件的类型：所有可订阅的事件名（与 Go 服务器
// protocol/events.go 对应）、WS 消息信封，以及每个事件对应的事件名 → payload
// 类型映射。

import type { Issue, IssueMetadata, IssueReaction } from "./issue";
import type { IssueProperty, IssuePropertyValues } from "./property";
import type { Agent } from "./agent";
import type { InboxItem } from "./inbox";
import type { Comment, Reaction } from "./comment";
import type { TimelineEntry } from "./activity";
import type { Workspace, MemberWithUser, Invitation } from "./workspace";
import type { Project } from "./project";
import type { Label } from "./label";

// WebSocket 事件类型（与 Go 服务器 protocol/events.go 一致）
export type WSEventType =
  | "issue:created"
  | "issue:updated"
  | "issue_attachments:changed"
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
  | "task:deferred"
  | "task:dispatch"
  | "task:running"
  | "task:waiting_local_directory"
  | "task:progress"
  | "task:completed"
  | "task:failed"
  | "task:message"
  | "task:cancelled"
  | "inbox:new"
  | "inbox:read"
  | "inbox:unread"
  | "inbox:archived"
  | "inbox:unarchived"
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
  | "chat:quick_actions"
  | "chat:cancel_finalized"
  | "chat:session_created"
  | "chat:session_read"
  | "chat:session_deleted"
  | "chat:session_updated"
  | "project:created"
  | "project:updated"
  | "project:deleted"
  | "squad:created"
  | "squad:updated"
  | "squad:deleted"
  | "label:created"
  | "label:updated"
  | "label:deleted"
  | "issue_labels:changed"
  | "issue_metadata:changed"
  | "issue_properties:changed"
  | "property:created"
  | "property:updated"
  | "issue_status:changed"
  | "pin:created"
  | "pin:deleted"
  | "pin:reordered"
  | "invitation:created"
  | "invitation:accepted"
  | "invitation:declined"
  | "invitation:revoked"
  | "github_installation:created"
  | "github_installation:deleted"
  | "pull_request:linked"
  | "pull_request:updated"
  | "pull_request:unlinked";

/** WS 消息信封：type + payload，另带可选的发起者身份。 */
export interface WSMessage<T = unknown> {
  type: WSEventType;
  payload: T;
  actor_id?: string;
  actor_type?: string;
}

/** 议题被创建时的事件载荷。 */
export interface IssueCreatedPayload {
  issue: Issue;
}

/** 议题被更新时的事件载荷。 */
export interface IssueUpdatedPayload {
  issue: Issue;
  // 服务器在 issue:updated 里标注了实际变更了哪些字段
  //（server/internal/handler/issue.go publish）。assignee_changed 让实时层在
  // 非成员变更时保留过滤后的 myList 缓存而不重新拉取；status_changed 让它在
  // 屏外（未加载）议题发生状态变更时对账看板列计数；project_changed 让它把
  // 被移动的议题从旧项目的过滤列表里去掉（乐观本地移动后客户端缓存差集
  // 不可靠——MUL-3669 / #4548）。其它变更标记在线路上也存在，需要时可在
  // 这里继续暴露。
  assignee_changed?: boolean;
  status_changed?: boolean;
  project_changed?: boolean;
}

/** 议题被删除时的事件载荷。 */
export interface IssueDeletedPayload {
  issue_id: string;
}

/** 议题标签变更时的事件载荷（带标签全集与议题修订号）。 */
export interface IssueLabelsChangedPayload {
  issue_id: string;
  labels: Label[];
  issue_revision?: number;
}

/** 议题附件变更时的事件载荷。 */
export interface IssueAttachmentsChangedPayload {
  issue_id: string;
  issue_revision?: number;
}

/** 议题元数据变更时的事件载荷。 */
export interface IssueMetadataChangedPayload {
  issue_id: string;
  metadata: IssueMetadata;
  issue_revision?: number;
}

/** 议题属性值变更时的事件载荷。 */
export interface IssuePropertiesChangedPayload {
  issue_id: string;
  properties: IssuePropertyValues;
  issue_revision?: number;
}

/** 属性目录（定义）变更时的事件载荷。 */
export interface PropertyChangedPayload {
  property: IssueProperty;
}

/**
 * workspace 议题状态目录变化了（MUL-6243）。
 *
 * 一个事件覆盖全部四种写操作，因为客户端的应答方式相同：重新读取目录。
 * 它刻意不携带条目——从事件里合并进某行，必须与客户端没见过的其它写操作
 * 做对账；而目录足够小，重新拉取既简单又安全。
 *
 * `action` 只是参考性的：让该帧在 devtools 里可自描述。没有任何逻辑路由它，
 * 所以即使将来出现客户端从未听过的新写入动词，也能正确刷新目录。
 */
export interface IssueStatusChangedPayload {
  action?: "created" | "updated" | "archived" | "reordered";
}

/** agent 状态变化时的事件载荷。 */
export interface AgentStatusPayload {
  agent: Agent;
}

/** agent 被创建时的事件载荷。 */
export interface AgentCreatedPayload {
  agent: Agent;
}

/** agent 被归档时的事件载荷。 */
export interface AgentArchivedPayload {
  agent: Agent;
}

/** agent 被恢复时的事件载荷。 */
export interface AgentRestoredPayload {
  agent: Agent;
}

/** 收件箱新增通知时的事件载荷。 */
export interface InboxNewPayload {
  item: InboxItem;
}

/** 通知被标为已读时的事件载荷。 */
export interface InboxReadPayload {
  item_id: string;
  recipient_id: string;
}

/** 接收者把通知重新标为未读时发出。 */
export interface InboxUnreadPayload {
  item_id: string;
  recipient_id: string;
}

/** 通知被归档时的事件载荷。 */
export interface InboxArchivedPayload {
  item_id: string;
  recipient_id: string;
}

/** 通知从归档恢复时的事件载荷。 */
export interface InboxUnarchivedPayload {
  item_id: string;
  issue_id: string | null;
  recipient_id: string;
}

/** 批量已读时的事件载荷。 */
export interface InboxBatchReadPayload {
  recipient_id: string;
  count: number;
}

/** 批量归档时的事件载荷。 */
export interface InboxBatchArchivedPayload {
  recipient_id: string;
  count: number;
}

/** 评论被创建时的事件载荷。 */
export interface CommentCreatedPayload {
  comment: Comment;
  issue_revision?: number;
}

/** 评论被更新时的事件载荷。 */
export interface CommentUpdatedPayload {
  comment: Comment;
  issue_revision?: number;
}

/** 评论被删除时的事件载荷。 */
export interface CommentDeletedPayload {
  comment_id: string;
  issue_id: string;
  issue_revision?: number;
}

/** 评论被解决时的事件载荷。 */
export interface CommentResolvedPayload {
  comment: Comment;
}

/** 评论取消解决时的事件载荷。 */
export interface CommentUnresolvedPayload {
  comment: Comment;
}

/** workspace 被更新时的事件载荷。 */
export interface WorkspaceUpdatedPayload {
  workspace: Workspace;
}

/** workspace 被删除时的事件载荷。 */
export interface WorkspaceDeletedPayload {
  workspace_id: string;
}

/** 成员被更新时的事件载荷。 */
export interface MemberUpdatedPayload {
  member: MemberWithUser;
}

/** 成员被添加时的事件载荷。 */
export interface MemberAddedPayload {
  member: MemberWithUser;
  workspace_id: string;
  workspace_name?: string;
}

/** 成员被移除时的事件载荷。 */
export interface MemberRemovedPayload {
  member_id: string;
  user_id: string;
  workspace_id: string;
}

/** 新增订阅者时的事件载荷。 */
export interface SubscriberAddedPayload {
  issue_id: string;
  user_type: string;
  user_id: string;
  reason: string;
}

/** 移除订阅者时的事件载荷。 */
export interface SubscriberRemovedPayload {
  issue_id: string;
  user_type: string;
  user_id: string;
}

/** 时间线新增活动时的事件载荷。 */
export interface ActivityCreatedPayload {
  issue_id: string;
  entry: TimelineEntry;
}

/** 任务的一条消息（流式输出、工具调用等）。 */
export interface TaskMessagePayload {
  id?: string;
  output_chars?: number;
  output_truncated?: boolean;
  /** 单次执行内的工具调用标识。 */
  call_id?: string;
  task_id: string;
  issue_id: string;
  chat_session_id?: string;
  seq: number;
  type: "text" | "thinking" | "tool_use" | "tool_result" | "error" | "runtime";
  tool?: string;
  content?: string;
  input?: Record<string, unknown>;
  output?: string;
  created_at?: string;
}

/** 任务生命周期事件的基础载荷。 */
export interface TaskLifecyclePayload {
  task_id: string;
  issue_id: string;
  chat_session_id?: string;
}

/** 任务入队时的事件载荷。 */
export interface TaskQueuedPayload {
  task_id: string;
  agent_id: string;
  issue_id: string;
  chat_session_id?: string;
  status: string;
}

/** 任务派发给 runtime 时的事件载荷。 */
export interface TaskDispatchPayload {
  task_id: string;
  agent_id: string;
  issue_id: string;
  runtime_id: string;
  chat_session_id?: string;
}

/** 任务开始运行的事件载荷。 */
export interface TaskRunningPayload {
  task_id: string;
  agent_id: string;
  issue_id: string;
  chat_session_id?: string;
  status: string;
}

// task:waiting_local_directory 在 daemon 取出任务但暂时无法取得磁盘路径锁时
// 发出——同一 daemon 上另一任务已在该 local_directory 里执行。`wait_reason`
// 指明目录及（若已知）占用它的任务短 id；StatusPill 据此渲染，让被停放的任务
// 自我解释而不是空转。它是展示名而绝不是绝对路径——daemon 在源头就去掉了
//（localDirectoryAssignment.DisplayName），因为这段文本会到达会话里的每个
// 客户端、并可能进入截图。
export interface TaskWaitingLocalDirectoryPayload {
  task_id: string;
  agent_id: string;
  issue_id: string;
  chat_session_id?: string;
  status: string;
  wait_reason?: string;
}

/** 任务完成时的事件载荷。 */
export interface TaskCompletedPayload {
  task_id: string;
  agent_id: string;
  issue_id: string;
  chat_session_id?: string;
  status: string;
}

/** 任务失败时的事件载荷。 */
export interface TaskFailedPayload {
  task_id: string;
  agent_id: string;
  issue_id: string;
  chat_session_id?: string;
  status: string;
  failure_reason?: string;
  retry_pending?: boolean;
}

/** 任务被取消时的事件载荷。 */
export interface TaskCancelledPayload {
  task_id: string;
  agent_id: string;
  issue_id: string;
  chat_session_id?: string;
  status: string;
}

/** 评论表情回应新增时的事件载荷。 */
export interface ReactionAddedPayload {
  reaction: Reaction;
  issue_id: string;
  comment_revision?: number;
}

/** 评论表情回应移除时的事件载荷。 */
export interface ReactionRemovedPayload {
  comment_id: string;
  issue_id: string;
  emoji: string;
  actor_type: string;
  actor_id: string;
  comment_revision?: number;
}

/** 议题表情回应新增时的事件载荷。 */
export interface IssueReactionAddedPayload {
  reaction: IssueReaction;
  issue_id: string;
  issue_revision?: number;
}

/** 议题表情回应移除时的事件载荷。 */
export interface IssueReactionRemovedPayload {
  issue_id: string;
  emoji: string;
  actor_type: string;
  actor_id: string;
  issue_revision?: number;
}

/** chat:message 实时事件载荷。 */
export interface ChatMessageEventPayload {
  chat_session_id: string;
  message_id: string;
  role: "user" | "assistant";
  content: string;
  task_id?: string;
  created_at: string;
}

/** chat:done 事件载荷（一轮对话完成）。 */
export interface ChatDonePayload {
  chat_session_id: string;
  task_id: string;
  /**
   * 服务器从刚持久化的助手 ChatMessage 行填充这些字段，让 WS 处理器能内联
   * 写入消息缓存。旧服务器（#2123 前）只发 chat_session_id + task_id；
   * 下面每个字段都按可选处理，缺失时回退到重新拉取。
   */
  message_id?: string;
  content?: string;
  elapsed_ms?: number;
  created_at?: string;
  /**
   * "message"（默认）或 "no_response"——一轮完成但无文本回复的直接对话
   *（MUL-4351）。可选/增量：旧服务器省略，消费方默认 "message"。由于直接
   * 对话完成现在总是恰好持久化一条助手行，即使 no_response 轮次也会同时带
   * message_id/content/created_at。
   */
  message_kind?: import("./chat").ChatMessageKind;
  /** 绑定到已持久化助手回复上的经服务器校验的跟进建议。 */
  quick_actions?: import("./chat").ChatQuickAction[];
  /**
   * daemon 将随后为这一轮补发 chat:quick_actions 补充事件——先渲染占位符。
   * 与已填充的 quick_actions 互斥；旧服务器/daemon 上缺失。
   */
  quick_actions_pending?: boolean;
}

/**
 * chat:quick_actions——用 daemon 后台轮生成的跟进建议补全一轮已完成的对话。
 * 空列表是终态：「本轮无建议」，可清除任何占位符。
 */
export interface ChatQuickActionsPayload {
  chat_session_id: string;
  task_id: string;
  message_id: string;
  quick_actions?: import("./chat").ChatQuickAction[];
  /**
   * 显式刷新背后的重新生成失败：此处携带的动作是该轮「未变化的」旧 pills，
   * 只是为了消掉 pending 转圈。客户端提示「无法刷新」，而不是把它们当新生成
   * 的。正常成功路径与旧服务器上为缺失/false。
   */
  failed?: boolean;
}

/**
 * 被取消聊天任务的延迟结果（#5219）。取消的 HTTP 响应无法携带它——空/非空
 * 判定要在 daemon 的 transcript 落盘后才能尘埃落定——所以服务器改在此广播：
 * outcome "stopped" 描述一条刚持久化的 "Stopped." 助手行（ChatDonePayload 形
 * 状字段），outcome "restored" 是纯失效提示——被删除的 prompt 本身在服务端
 * 持久留存，由发起方的客户端从 creator 授权的草稿恢复端点拉取。判别字段之外
 * 的所有字段都可选——防御式处理，缺失时回退到重新拉取。
 */
export interface ChatCancelFinalizedPayload {
  outcome: "stopped" | "restored";
  chat_session_id: string;
  task_id: string;
  /**
   * 触发该取消任务的用户。只有这个用户的客户端需要重新拉取草稿恢复；缺失值
   * 按「不是我」处理（fail-closed——持久恢复仍会在下次打开会话时被取到）。
   */
  initiator_user_id?: string;
  message_id?: string;
  /** "Stopped." 助手行字段——仅 outcome "stopped" 时设置。 */
  content?: string;
  message_kind?: import("./chat").ChatMessageKind;
  created_at?: string;
  elapsed_ms?: number;
}

/** 会话被标为已读的事件载荷。 */
export interface ChatSessionReadPayload {
  chat_session_id: string;
}

/** 会话被删除的事件载荷。 */
export interface ChatSessionDeletedPayload {
  chat_session_id: string;
}

/** 项目被创建时的事件载荷。 */
export interface ProjectCreatedPayload {
  project: Project;
}

/** 项目被更新时的事件载荷。 */
export interface ProjectUpdatedPayload {
  project: Project;
}

/** 项目被删除时的事件载荷。 */
export interface ProjectDeletedPayload {
  project_id: string;
}

/** 邀请被创建时的事件载荷。 */
export interface InvitationCreatedPayload {
  invitation: Invitation;
  workspace_name?: string;
}

/** 邀请被接受时的事件载荷。 */
export interface InvitationAcceptedPayload {
  invitation_id: string;
  member: MemberWithUser;
}

/** 邀请被拒绝时的事件载荷。 */
export interface InvitationDeclinedPayload {
  invitation_id: string;
  invitee_email: string;
}

/** 邀请被撤销时的事件载荷。 */
export interface InvitationRevokedPayload {
  invitation_id: string;
  invitee_email: string;
}

/** 外部渠道创建了会话的事件载荷。 */
export interface ChatSessionCreatedPayload {
  workspace_id: string;
  chat_session_id: string;
  agent_id: string;
  creator_id: string;
  title: string;
  channel_source: {
    channel_type: string;
    installation_id: string;
    route_revision: number;
  };
  is_current_channel_route: boolean;
}

/**
 * 把每个 WSEventType 映射到它的 payload 接口。payload 形状没有正式类型化的
 * 事件（服务器发出的对象客户端目前只是当作不透明触发器消费）回退为 `unknown`
 * ——调用方必须先收窄再访问。
 *
 * 建议通过 `WSEventPayload<E>` 使用而非直接索引该映射：
 *   const handler = (payload: WSEventPayload<"issue:created">) => { ... };
 *
 * 新增事件：先扩展上面的 WSEventType，然后在此追加一个键。TS 会对任何忘了
 * payload 形状的 WSClient.on("new:event", …) 调用点报编译错——这正是目的所在。
 */
export interface WSEventPayloadMap {
  "issue:created": IssueCreatedPayload;
  "issue:updated": IssueUpdatedPayload;
  "issue:deleted": IssueDeletedPayload;
  "issue_attachments:changed": IssueAttachmentsChangedPayload;
  "issue_labels:changed": IssueLabelsChangedPayload;
  "issue_properties:changed": IssuePropertiesChangedPayload;
  "property:created": PropertyChangedPayload;
  "property:updated": PropertyChangedPayload;
  "issue_status:changed": IssueStatusChangedPayload;
  "issue_reaction:added": IssueReactionAddedPayload;
  "issue_reaction:removed": IssueReactionRemovedPayload;
  "comment:created": CommentCreatedPayload;
  "comment:updated": CommentUpdatedPayload;
  "comment:deleted": CommentDeletedPayload;
  "comment:resolved": CommentResolvedPayload;
  "comment:unresolved": CommentUnresolvedPayload;
  "reaction:added": ReactionAddedPayload;
  "reaction:removed": ReactionRemovedPayload;
  "agent:status": AgentStatusPayload;
  "agent:created": AgentCreatedPayload;
  "agent:archived": AgentArchivedPayload;
  "agent:restored": AgentRestoredPayload;
  "task:queued": TaskQueuedPayload;
  "task:deferred": TaskQueuedPayload;
  "task:dispatch": TaskDispatchPayload;
  "task:running": TaskRunningPayload;
  "task:waiting_local_directory": TaskWaitingLocalDirectoryPayload;
  "task:completed": TaskCompletedPayload;
  "task:failed": TaskFailedPayload;
  "task:message": TaskMessagePayload;
  "task:cancelled": TaskCancelledPayload;
  "task:progress": unknown;
  "inbox:new": InboxNewPayload;
  "inbox:read": InboxReadPayload;
  "inbox:unread": InboxUnreadPayload;
  "inbox:archived": InboxArchivedPayload;
  "inbox:unarchived": InboxUnarchivedPayload;
  "inbox:batch-read": InboxBatchReadPayload;
  "inbox:batch-archived": InboxBatchArchivedPayload;
  "workspace:updated": WorkspaceUpdatedPayload;
  "workspace:deleted": WorkspaceDeletedPayload;
  "member:added": MemberAddedPayload;
  "member:updated": MemberUpdatedPayload;
  "member:removed": MemberRemovedPayload;
  "subscriber:added": SubscriberAddedPayload;
  "subscriber:removed": SubscriberRemovedPayload;
  "activity:created": ActivityCreatedPayload;
  "chat:message": ChatMessageEventPayload;
  "chat:done": ChatDonePayload;
  "chat:quick_actions": ChatQuickActionsPayload;
  "chat:cancel_finalized": ChatCancelFinalizedPayload;
  "chat:session_created": ChatSessionCreatedPayload;
  "chat:session_read": ChatSessionReadPayload;
  "chat:session_deleted": ChatSessionDeletedPayload;
  "chat:session_updated": unknown;
  "project:created": ProjectCreatedPayload;
  "project:updated": ProjectUpdatedPayload;
  "project:deleted": ProjectDeletedPayload;
  "invitation:created": InvitationCreatedPayload;
  "invitation:accepted": InvitationAcceptedPayload;
  "invitation:declined": InvitationDeclinedPayload;
  "invitation:revoked": InvitationRevokedPayload;
  // 尚无正式 payload 接口——服务器发出领域对象，客户端当前只把它们当作
  // 不透明触发器消费（收到即重新拉取）。
  "daemon:heartbeat": unknown;
  "daemon:register": unknown;
  "skill:created": unknown;
  "skill:updated": unknown;
  "skill:deleted": unknown;
  "squad:created": unknown;
  "squad:updated": unknown;
  "squad:deleted": unknown;
  "label:created": unknown;
  "label:updated": unknown;
  "label:deleted": unknown;
  "pin:created": unknown;
  "pin:deleted": unknown;
  "pin:reordered": unknown;
  "github_installation:created": unknown;
  "github_installation:deleted": unknown;
  "pull_request:linked": unknown;
  "pull_request:updated": unknown;
  "pull_request:unlinked": unknown;
}

/**
 * 指定事件的 payload 类型。在 WSEventPayloadMap 上查找，以 `unknown` 作安全
 * 网——若未来新增 WSEventType 却没加映射项，调用方看到 `unknown`（被迫收窄）
 * 而不是 `any`（静默的不安全访问）。
 */
export type WSEventPayload<E extends WSEventType> =
  E extends keyof WSEventPayloadMap ? WSEventPayloadMap[E] : unknown;
