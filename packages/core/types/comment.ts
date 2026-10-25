// 本文件定义「评论（Comment）」相关类型：评论本体、表情回应、以及
// @agent/@squad 提及触发的预览与结果。

/** 评论类型：普通评论 / 状态变更 / 进度更新 / 系统生成。 */
export type CommentType = "comment" | "status_change" | "progress_update" | "system";

// `system` 用于平台生成的记录行（如父议题的「子议题完成」通知，MUL-2538）。
// 系统行的 author_id 是零 UUID；渲染路径应依据 author_type 分支，而不是 UUID。
export type CommentAuthorType = "member" | "agent" | "system";

/** 评论上的一个表情回应。 */
export interface Reaction {
  id: string;
  comment_id: string;
  actor_type: string;
  actor_id: string;
  emoji: string;
  created_at: string;
  comment_revision?: number;
}

/** 一条评论。 */
export interface Comment {
  id: string;
  issue_id: string;
  author_type: CommentAuthorType;
  author_id: string;
  content: string;
  type: CommentType;
  parent_id: string | null;
  reactions: Reaction[];
  attachments: import("./attachment").Attachment[];
  created_at: string;
  updated_at: string;
  /** 服务端单调递增的修订号；连到旧后端时缺失。 */
  revision?: number;
  /** 该语义评论变更后的父议题修订号。 */
  issue_revision?: number;
  resolved_at: string | null;
  resolved_by_type: CommentAuthorType | null;
  resolved_by_id: string | null;
  source_task_id?: string | null;
  // 生成这条评论的快捷动作 id（MUL-5465）。快捷动作发布一条 ORDINARY 评论并
  // 打上此 id；折叠卡片按 id 而不是专门的 `type` 判断，因为 `type` 在通用评论
  // 端点上是客户端传入的，会被伪造。
  quick_action_id?: string | null;
  // Set only on a comment deleted while it still had replies (#8296): the
  // server keeps an empty tombstone so the replies keep their parent. Older
  // servers omit it.
  deleted_at?: string | null;
  // Per-target result of every explicit @agent / @squad mention in this comment
  // (MUL-4525 §2). Present only on create/edit responses; older servers omit it.
  trigger_outcomes?: CommentTriggerOutcome[];
  // 回复托管审查员时，实际接收这条回复的人及其派发结果（GONG-63）。审查员只接受
  // 系统派发的审查任务，因此回复会转给任务当前负责人。只在创建/编辑响应里出现；
  // 旧服务器省略它。
  review_reply?: CommentReviewReply | null;
}

// 单个被显式提及的触发目标的领域结果。「成功形态」的状态
//（queued/coalesced/deferred）表示提及已被处理；`blocked` 表示以枚举安全的
// reason_code 被拒绝。
export type CommentTriggerStatus =
  | "queued"
  | "coalesced"
  | "deferred"
  | "blocked";

/** 评论中一个提及目标的处理结果。 */
export interface CommentTriggerOutcome {
  target_type: string; // "agent" | "squad"
  target_id: string;
  status: CommentTriggerStatus | string;
  reason_code: string;
}

/** 提及来源：议题经办人 / 提及某 agent / 提及某 squad 队长。 */
export type CommentTriggerSource =
  | "issue_assignee"
  | "mention_agent"
  | "mention_squad_leader"
  // 回复审查员时转交给任务当前负责人（GONG-63）。
  | "review_followup_assignee";

/**
 * 回复托管审查员的转交结果。`status` 说明实际发生了什么：预览阶段是 `will_notify`
 * （会通知负责人）或人类/无负责人/被拦；保存后是 `queued` / `coalesced` /
 * `deferred`（运行已排队或合并）或 `blocked`（附 `reason_code`）。
 */
export interface CommentReviewReply {
  recipient_type?: string; // "agent" | "squad" | "member"
  recipient_id?: string;
  recipient_name?: string;
  status: string;
  reason_code?: string;
}

/** 触发预览中的单个 agent：谁会被触发 + 触发来源与原因。 */
export interface CommentTriggerPreviewAgent {
  id: string;
  name: string;
  avatar_url?: string;
  source: CommentTriggerSource | string;
  reason: string;
}

/** 评论触发预览响应：这条评论原样发布会触发哪些 agent。 */
export interface CommentTriggerPreview {
  agents: CommentTriggerPreviewAgent[];
  // 原样发布时「不会」触发的显式 @agent/@squad 提及（MUL-4525 §2）。
  // 增量字段：旧服务器省略。
  blocked?: CommentTriggerOutcome[];
  // 回复审查员时这条草稿会通知谁（GONG-63）。增量字段：旧服务器省略。
  review_reply?: CommentReviewReply | null;
}
