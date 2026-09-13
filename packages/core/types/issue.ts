// 本文件定义「议题（Issue）」核心类型：状态类别/状态键、优先级、经办人、
// 元数据、来源上下文（source context）与议题本体。

import type { Label } from "./label";
import type { IssuePropertyValues } from "./property";

/**
 * 状态「类别」——议题状态所属的行为等价类。恰好有 7 个，每个同时也是定义
 * 它的内建状态的 key，因此它保持封闭联合而 `Issue.status` 变成开放值。
 * 看板列、筛选与展示配置都以类别为键，所以无论 workspace 定义多少自定义
 * 状态，类别形状都固定。（MUL-6243）
 */
export type IssueStatusCategory =
  | "backlog"
  | "todo"
  | "in_progress"
  | "in_review"
  | "done"
  | "blocked"
  | "cancelled";

/**
 * 存在议题上的状态 KEY：7 个内建之一，或管理员为本 workspace 定义的自定义
 * key。
 *
 * 刻意开放。`(string & {})` 在保留 7 个内建值的编辑器自动补全的同时，接受
 * 目录里的任意 key——这正是服务器一直能发送的。任何需要展示的（标签、颜色、
 * 看板列）都必须先把 key 解析到其「类别」——组件里用 `useIssueStatuses(wsId)`，
 * 纯路径里用 `statusCategoryOfKey`。（MUL-6243）
 */
export type IssueStatus = IssueStatusCategory | (string & {});

/** 议题优先级。 */
export type IssuePriority = "urgent" | "high" | "medium" | "low" | "none";

/** 议题经办人/创建者类型：成员、agent 或 squad。 */
export type IssueAssigneeType = "member" | "agent" | "squad";

/** 议题上的一个表情回应。 */
export interface IssueReaction {
  id: string;
  issue_id: string;
  actor_type: string;
  actor_id: string;
  emoji: string;
  created_at: string;
  issue_revision?: number;
}

/**
 * 议题级元数据是扁平 KV 映射，agent 用它记录流水线状态（PR number、
 * pipeline_status、waiting_on……）。值只允许原始类型——string / number /
 * bool——API 与 DB 双层强制。响应里始终存在（未设置时空对象），读取方对父
 * 字段无需判空。
 */
export type IssueMetadataValue = string | number | boolean;

/** 议题元数据映射：键 → 标量值。agent 用它记录流水线状态等键值信息。 */
export type IssueMetadata = Record<string, IssueMetadataValue>;

/** 来源上下文里的一个附件快照。 */
export interface SourceContextAttachment {
  id: string;
  source_attachment_id?: string;
  owner_type: "issue" | "comment" | (string & {});
  owner_id: string;
  filename: string;
  content_type: string;
  size_bytes: number;
  created_at: string;
}

/** 来源上下文的作者（成员或 agent）。 */
export interface SourceContextAuthor {
  type: "member" | "agent" | (string & {});
  id: string;
  name: string;
}

/** 来源议题的快照（生成子议题时捕获）。 */
export interface SourceContextIssueSnapshot {
  id: string;
  identifier: string;
  number: number;
  title: string;
  description: string | null;
  created_at: string;
  updated_at: string;
  revision: number;
  attachments: SourceContextAttachment[];
}

/** 来源评论线程中一条评论的快照。 */
export interface SourceContextCommentSnapshot {
  id: string;
  parent_id: string | null;
  type: string;
  content: string;
  author: SourceContextAuthor;
  created_at: string;
  updated_at: string;
  revision: number;
  attachments: SourceContextAttachment[];
}

/** 议题的来源上下文快照：源议题 + 评论线程 + 锚点评论。 */
export interface SourceContextSnapshot {
  /** 捕获元数据只存在于持久化的详情快照上；提交前的预览载荷省略它。 */
  version?: number;
  captured_by_user_id?: string;
  captured_at?: string;
  source_issue: SourceContextIssueSnapshot;
  comment_thread: SourceContextCommentSnapshot[];
  anchor_comment_id: string;
}

/** 来源上下文的用量统计。 */
export interface SourceContextLimitUsage {
  comment_count: number;
  text_bytes: number;
  attachment_count: number;
  attachment_bytes: number;
}

/** 来源上下文预览：快照 + 一次性捕获 token + 用量上限。 */
export interface SourceContextPreview extends SourceContextSnapshot {
  capture_token: string;
  limits: SourceContextLimitUsage;
}

/** 来源里某个作者在捕获时 vs 当前的名字/状态。 */
export interface SourceContextAuthorState {
  type: string;
  id: string;
  captured_name: string;
  current_name?: string;
  state: string;
}

/** 描述区附件的一次变更（增/删/替换）。 */
export interface SourceContextDescriptionAttachmentChange {
  kind: "added" | "removed" | "replaced" | (string & {});
  attachment_id: string;
  filename: string;
  previous_filename?: string;
}

/** 来源相对当前状态的具体差异明细。 */
export interface SourceContextChangeDetails {
  changed_comment_ids: string[];
  added_comments?: SourceContextCommentSnapshot[];
  removed_comment_ids?: string[];
  description_attachment_changes: SourceContextDescriptionAttachmentChange[];
}

/** 议题的来源上下文详情：当前源与捕获快照的对比及差异。 */
export interface IssueSourceContext {
  id: string;
  version: number;
  usage: "read_only_historical_background" | (string & {});
  captured_at: string;
  display_state: "unchanged" | "changed" | "deleted" | "unavailable" | (string & {});
  source_issue_state: "unchanged" | "changed" | "deleted" | "unavailable" | (string & {});
  comment_thread_state: "unchanged" | "changed" | "unavailable" | (string & {});
  anchor_comment_state: "available" | "deleted" | "unavailable" | (string & {});
  can_open_current_source: boolean;
  change_reasons?: string[];
  change_details?: SourceContextChangeDetails;
  current_source?: { issue_id: string; identifier: string; anchor_comment_id: string };
  source_author_state?: SourceContextAuthorState[];
  snapshot: SourceContextSnapshot;
}

/** 一条议题。 */
export interface Issue {
  id: string;
  workspace_id: string;
  number: number;
  identifier: string;
  title: string;
  description: string | null;
  status: IssueStatus;
  /**
   * `status` 所属的类别（当端点已解析它时）。可选：内建状态本身就是其类别、
   * 无需解析——请用 `issueStatusCategory(issue)` 而不是直接读此字段。
   *（MUL-6243）
   */
  status_category?: IssueStatusCategory;
  /**
   * 自定义状态的显示名，随 key 一起携带。7 个内建状态为空，它们从 key 做
   * 本地化——优先用 `useStatusLabel`，它两者都能处理且管理员重命名状态后
   * 仍保持正确。
   *
   * 仅为兼容早于它的服务器而可选；当前服务器始终发送该字段。（MUL-6749）
   */
  status_name?: string;
  priority: IssuePriority;
  assignee_type: IssueAssigneeType | null;
  assignee_id: string | null;
  creator_type: IssueAssigneeType;
  creator_id: string;
  parent_issue_id: string | null;
  project_id: string | null;
  project?: {
    id: string;
    title: string;
    icon: string | null;
  } | null;
  child_progress?: {
    done: number;
    total: number;
  };
  agent_activity?: {
    running_count: number;
    queued_count: number;
    agent_ids: string[];
  };
  position: number;
  // 兄弟子议题中的有序「关卡」分组（null = 未分关）。只有某一关的所有子议题
  // 都完成时才通知/唤醒父议题经办人；见 server/internal/handler/issue_child_done.go。
  stage: number | null;
  // 日历日期，仅日期 "YYYY-MM-DD"（无时刻、无时区）。用
  // @multica/core/issues/date 里的辅助函数做格式化/比较——绝不要
  // `new Date()` + 本地格式化，那会随查看者时区偏移日期。
  start_date: string | null;
  due_date: string | null;
  metadata: IssueMetadata;
  // 自定义属性值，按属性定义 id 为键。响应里始终存在（未设置时空对象），
  // 与 `metadata` 保持一致。
  properties: IssuePropertyValues;
  reactions?: IssueReaction[];
  labels?: Label[];
  created_at: string;
  updated_at: string;
  /** 服务端单调递增修订号；连到旧后端时缺失。 */
  revision?: number;
  /**
   * 在服务端历史活动回填到该行之前为 null。此 RFC3339 时间戳可能含亚秒
   * 精度，而旧的 created_at/updated_at 是秒精度；比较前先解析。
   */
  last_activity_at?: string | null;
  /** 仅「由评论创建」的议题详情响应里出现。 */
  source_context?: IssueSourceContext;
}
