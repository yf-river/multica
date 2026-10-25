// 本文件定义议题时间线（Timeline）相关类型：活动记录与评论的混合列表。

import type { CommentAuthorType, Reaction } from "./comment";
import type { Attachment } from "./attachment";

/** 指派频率统计条目：某个指派对象在统计期内的处理次数。 */
export interface AssigneeFrequencyEntry {
  assignee_type: string;
  assignee_id: string;
  frequency: number;
}

/** 时间线上的单条记录：可能是操作活动（activity），也可能是评论（comment）。 */
export interface TimelineEntry {
  type: "activity" | "comment";
  id: string;
  actor_type: string;
  actor_id: string;
  created_at: string;
  /** Display identity hydrated from the actor's global user row when available. */
  actor_name?: string;
  actor_avatar_url?: string;
  // Activity fields
  action?: string;
  details?: Record<string, unknown>;
  // Comment（评论）字段
  content?: string;
  parent_id?: string | null;
  updated_at?: string;
  revision?: number;
  comment_type?: string;
  /** 仅由快捷动作产生的评论会带此 id（MUL-5465），不可伪造。 */
  quick_action_id?: string | null;
  reactions?: Reaction[];
  attachments?: Attachment[];
  resolved_at?: string | null;
  resolved_by_type?: CommentAuthorType | null;
  resolved_by_id?: string | null;
  source_task_id?: string | null;
  /**
   * Set only on a comment deleted while it still had replies: the server keeps
   * it as an empty tombstone so the replies keep their parent. Read it through
   * `isDeletedComment`.
   */
  deleted_at?: string | null;
  /** Set by frontend coalescing when consecutive identical activities are merged. */
  coalesced_count?: number;
}
