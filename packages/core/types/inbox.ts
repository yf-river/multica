// 本文件定义「收件箱（Inbox）」相关类型：站内通知的条目、严重级别与类型。

import type { IssuePriority, IssueStatus } from "./issue";

/** 收件箱条目的严重程度：需处理 / 需关注 / 仅告知。 */
export type InboxSeverity = "action_required" | "attention" | "info";

/** 收件箱条目类型：由什么动作触发（指派、状态变更、新评论、任务结果等）。 */
export type InboxItemType =
  | "issue_assigned"
  | "issue_subscribed"
  | "unassigned"
  | "assignee_changed"
  | "status_changed"
  | "priority_changed"
  | "start_date_changed"
  | "due_date_changed"
  | "new_comment"
  | "mentioned"
  | "review_requested"
  | "task_completed"
  | "task_failed"
  | "agent_blocked"
  | "agent_completed"
  | "reaction_added"
  | "quick_create_done"
  | "quick_create_failed"
  // 快速创建结果无法确认的类型。与 quick_create_failed 不同，它不能按
  // 「失败」的文案框架渲染：议题可能实际上已经创建成功了。
  | "quick_create_unconfirmed";

/**
 * 跨 workspace 汇总里「某个 workspace 的未读数」。
 *（GET /api/inbox/unread-summary）侧边栏用它在 workspace 切换器上给
 * 「非当前活跃的」workspace 点亮未读圆点。
 */
export interface InboxWorkspaceUnread {
  workspace_id: string;
  count: number;
}

/** 一条收件箱通知。 */
export interface InboxItem {
  id: string;
  workspace_id: string;
  recipient_type: "member" | "agent";
  recipient_id: string;
  actor_type: "member" | "agent" | "system" | null;
  actor_id: string | null;
  type: InboxItemType;
  severity: InboxSeverity;
  issue_id: string | null;
  title: string;
  body: string | null;
  issue_status: IssueStatus | null;
  /**
   * 关联议题的当前优先级。设为可选以保证已安装的 Desktop 客户端与早于
   * 该收件箱投影的旧后端兼容；null 也覆盖「没有关联议题」的通知。
   */
  issue_priority?: IssuePriority | null;
  read: boolean;
  archived: boolean;
  created_at: string;
  details: Record<string, string> | null;
}
