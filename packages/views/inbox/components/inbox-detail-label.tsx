"use client";

// 收件箱条目「正文标签」渲染器：把各种通知类型（状态/优先级变更、评论、提及、
// 快速创建结果等）的细节字段翻译成一行可读的短文案。
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { formatDateOnly } from "@multica/core/issues/date";
import { useActorName } from "@multica/core/workspace/hooks";
import { StatusIcon } from "../../issues/components/status-icon";
import { PriorityIcon } from "../../issues/components/priority-icon";
import type { InboxItem, InboxItemType, IssueStatus, IssuePriority } from "@multica/core/types";
import { getQuickCreateOutcomeDetail } from "./inbox-display";
import { useLocale, useT } from "../../i18n";
import { useStatusLabel } from "../../issues/utils/status-label";
import { priorityLabel } from "../../issues/utils/priority-label";

// 返回「收件箱条目类型 → 中文文案」的 Hook。取代旧的静态 `typeLabels` 常量，
// 让文案走 i18next；调用方保持 `typeLabels[type]` 的取法不变。
/** 返回各收件箱条目类型对应的本地化类型名。 */
export function useTypeLabels(): Record<InboxItemType, string> {
  const { t } = useT("inbox");
  return {
    issue_assigned: t(($) => $.types.issue_assigned),
    issue_subscribed: t(($) => $.types.issue_subscribed),
    unassigned: t(($) => $.types.unassigned),
    assignee_changed: t(($) => $.types.assignee_changed),
    status_changed: t(($) => $.types.status_changed),
    priority_changed: t(($) => $.types.priority_changed),
    start_date_changed: t(($) => $.types.start_date_changed),
    due_date_changed: t(($) => $.types.due_date_changed),
    new_comment: t(($) => $.types.new_comment),
    mentioned: t(($) => $.types.mentioned),
    review_requested: t(($) => $.types.review_requested),
    task_completed: t(($) => $.types.task_completed),
    task_failed: t(($) => $.types.task_failed),
    agent_blocked: t(($) => $.types.agent_blocked),
    agent_completed: t(($) => $.types.agent_completed),
    reaction_added: t(($) => $.types.reaction_added),
    quick_create_done: t(($) => $.types.quick_create_done),
    quick_create_failed: t(($) => $.types.quick_create_failed),
    quick_create_unconfirmed: t(($) => $.types.quick_create_unconfirmed),
  };
}

// start_date / due_date 是日历日——按时区安全方式格式化，避免随查看者时区偏移
// 而串日（见 @multica/core/issues/date）。
function shortDate(dateStr: string, locale: string): string {
  return formatDateOnly(dateStr, { month: "short", day: "numeric" }, locale);
}

/**
 * 收件箱条目的正文标签：按条目类型从 details 里渲染出一行可读文案
 * （如「将状态设为 ✓ 进行中」「把任务分配给了 张三」）。
 *
 * @param item 收件箱条目
 */
export function InboxDetailLabel({ item }: { item: InboxItem }) {
  const { t } = useT("inbox");
  const { t: tIssues } = useT("issues");
  const locale = useLocale();
  const typeLabels = useTypeLabels();
  const { getActorName } = useActorName();
  // 收件箱是跨工作区的界面，所以状态目录按条目自己的工作区读取，而不是按路由。
  // (MUL-6243)
  const { categoryOf, colorOf } = useIssueStatuses(item.workspace_id);
  const statusLabelOf = useStatusLabel(item.workspace_id);
  const details = item.details ?? {};

  switch (item.type) {
    case "status_changed": {
      if (!details.to) return <span>{typeLabels[item.type]}</span>;
      return (
        <span className="inline-flex items-center gap-1">
          {t(($) => $.labels.set_status_to)}
          <StatusIcon
            status={details.to as IssueStatus}
            category={categoryOf(details.to)}
            color={colorOf(details.to)}
            className="h-3 w-3"
          />
          {statusLabelOf(details.to)}
        </span>
      );
    }
    case "priority_changed": {
      if (!details.to) return <span>{typeLabels[item.type]}</span>;
      const label = priorityLabel(details.to, tIssues);
      return (
        <span className="inline-flex items-center gap-1">
          {t(($) => $.labels.set_priority_to)}
          <PriorityIcon priority={details.to as IssuePriority} className="h-3 w-3" />
          {label}
        </span>
      );
    }
    case "issue_assigned": {
      if (details.new_assignee_id) {
        return <span>{t(($) => $.labels.assigned_to, { name: getActorName(details.new_assignee_type ?? "member", details.new_assignee_id) })}</span>;
      }
      return <span>{typeLabels[item.type]}</span>;
    }
    case "unassigned":
      return <span>{t(($) => $.labels.removed_assignee)}</span>;
    case "assignee_changed": {
      if (details.new_assignee_id) {
        return <span>{t(($) => $.labels.assigned_to, { name: getActorName(details.new_assignee_type ?? "member", details.new_assignee_id) })}</span>;
      }
      return <span>{typeLabels[item.type]}</span>;
    }
    case "start_date_changed": {
      if (details.to) return <span>{t(($) => $.labels.set_start_date_to, { date: shortDate(details.to, locale) })}</span>;
      return <span>{t(($) => $.labels.removed_start_date)}</span>;
    }
    case "due_date_changed": {
      if (details.to) return <span>{t(($) => $.labels.set_due_date_to, { date: shortDate(details.to, locale) })}</span>;
      return <span>{t(($) => $.labels.removed_due_date)}</span>;
    }
    case "new_comment": {
      if (item.body) return <span>{item.body}</span>;
      return <span>{typeLabels[item.type]}</span>;
    }
    case "reaction_added": {
      const emoji = details.emoji;
      if (emoji) return <span>{t(($) => $.labels.reacted_to_comment, { emoji })}</span>;
      return <span>{typeLabels[item.type]}</span>;
    }
    case "quick_create_done": {
      const identifier = details.identifier;
      if (identifier) return <span>{t(($) => $.labels.created_with_agent, { identifier })}</span>;
      return <span>{typeLabels[item.type]}</span>;
    }
    case "quick_create_failed": {
      const detail = getQuickCreateOutcomeDetail(item);
      if (detail) return <span>{t(($) => $.labels.failed_with_detail, { detail })}</span>;
      return <span>{typeLabels[item.type]}</span>;
    }
    case "quick_create_unconfirmed": {
      // 刻意不用 failed_with_detail 的文案：结果未知，所以详情原样展示，
      // 不套「失败:」的框。
      const detail = getQuickCreateOutcomeDetail(item);
      if (detail) return <span>{detail}</span>;
      return <span>{typeLabels[item.type]}</span>;
    }
    default:
      return <span>{typeLabels[item.type] ?? item.type}</span>;
  }
}
