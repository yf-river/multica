/**
 * Mobile InboxDetailLabel — type-aware second-line for inbox rows.
 *
 * Mirrors packages/views/inbox/components/inbox-detail-label.tsx exactly:
 * for each InboxItemType the user sees the same label they would see on
 * web/desktop. This is a Behavioral parity concern — if web shows "Set
 * status to ✓ Done", mobile must show "Set status to ✓ Done" (rendered
 * with mobile primitives, not the literal HTML).
 *
 * Web is i18n-driven (useT). Mobile v1 is English-only; when mobile ships
 * i18n, mirror the namespace structure.
 */
import { View } from "react-native";
import type {
  InboxItem,
  InboxItemType,
  IssuePriority,
} from "@multica/core/types";
import { formatDateOnly } from "@multica/core/issues/date";
import { Text } from "@/components/ui/text";
import { StatusIcon } from "@/components/ui/status-icon";
import { PriorityIcon } from "@/components/ui/priority-icon";
import { useActorLookup } from "@/data/use-actor-name";
import { useIssueStatuses } from "@/lib/use-issue-statuses";
import { cn } from "@/lib/utils";

// Mirrors PRIORITY_CONFIG.label in packages/core/issues/config/priority.ts
const PRIORITY_LABEL: Record<IssuePriority, string> = {
  urgent: "紧急",
  high: "高",
  medium: "中",
  low: "低",
  none: "无优先级",
};

// Mirrors useTypeLabels in packages/views/inbox/components/inbox-detail-label.tsx
const TYPE_LABEL: Record<InboxItemType, string> = {
  issue_assigned: "已分配",
  issue_subscribed: "已订阅",
  unassigned: "未分配",
  assignee_changed: "已重新分配",
  status_changed: "状态已更改",
  priority_changed: "优先级已更改",
  start_date_changed: "开始日期已更改",
  due_date_changed: "截止日期已更改",
  new_comment: "新评论",
  mentioned: "提到了你",
  review_requested: "请求审查",
  task_completed: "任务已完成",
  task_failed: "任务失败",
  agent_blocked: "智能体已阻塞",
  agent_completed: "智能体已完成",
  reaction_added: "添加了回应",
  quick_create_done: "快速创建已完成",
  quick_create_failed: "快速创建失败",
  quick_create_unconfirmed: "快速创建结果待确认",
};

// due_date is a calendar day — format timezone-safely (no offset day shift).
function shortDate(dateStr: string): string {
  return formatDateOnly(dateStr, { month: "short", day: "numeric" }, "zh-CN");
}

function singleLine(value: string | null | undefined): string {
  return (value ?? "").replace(/\s+/g, " ").trim();
}

export function InboxDetailLabel({
  item,
  className,
}: {
  item: InboxItem;
  className?: string;
}) {
  const { getName } = useActorLookup();
  // `details.to` is a status KEY and may be a custom one, so its name, colour
  // and glyph all resolve through the workspace catalog. (MUL-6243)
  const { categoryOf, colorOf, labelOf } = useIssueStatuses();
  const details = item.details ?? {};

  // Cases with inline icons → Row layout.
  if (item.type === "status_changed" && details.to) {
    const status = details.to;
    return (
      <View className={cn("flex-row items-center gap-1", className)}>
        <Text className="text-xs text-muted-foreground">将状态设为</Text>
        <StatusIcon
          status={status}
          category={categoryOf(status)}
          color={colorOf(status)}
          size={12}
        />
        <Text className="text-xs text-muted-foreground" numberOfLines={1}>
          {labelOf(status)}
        </Text>
      </View>
    );
  }

  if (item.type === "priority_changed" && details.to) {
    const priority = details.to as IssuePriority;
    return (
      <View className={cn("flex-row items-center gap-1", className)}>
        <Text className="text-xs text-muted-foreground">将优先级设为</Text>
        <PriorityIcon priority={priority} size={12} />
        <Text className="text-xs text-muted-foreground" numberOfLines={1}>
          {PRIORITY_LABEL[priority] ?? priority}
        </Text>
      </View>
    );
  }

  // Single-string cases.
  const text = (() => {
    switch (item.type) {
      case "issue_assigned":
      case "assignee_changed":
        if (details.new_assignee_id) {
          const name = getName(
            (details.new_assignee_type ?? "member") as "member" | "agent",
            details.new_assignee_id,
          );
          return `已分配给 ${name}`;
        }
        return TYPE_LABEL[item.type];
      case "unassigned":
        return "已移除负责人";
      case "due_date_changed":
        return details.to
          ? `截止日期设为 ${shortDate(details.to)}`
          : "已移除截止日期";
      case "new_comment":
        return singleLine(item.body) || TYPE_LABEL[item.type];
      case "reaction_added":
        return details.emoji
          ? `回应了 ${details.emoji}`
          : TYPE_LABEL[item.type];
      case "quick_create_done":
        return details.identifier
          ? `智能体已创建：${details.identifier}`
          : TYPE_LABEL[item.type];
      case "quick_create_failed": {
        const detail = singleLine(details.error) || singleLine(item.body);
        return detail ? `失败：${detail}` : TYPE_LABEL[item.type];
      }
      // Mirrors packages/views/inbox/components/inbox-detail-label.tsx: the
      // unconfirmed outcome deliberately drops the "Failed:" prefix, because
      // the issue may actually have been created.
      case "quick_create_unconfirmed": {
        const detail = singleLine(details.error) || singleLine(item.body);
        return detail || TYPE_LABEL[item.type];
      }
      default:
        return TYPE_LABEL[item.type] ?? item.type;
    }
  })();

  return (
    <Text
      className={cn("text-xs text-muted-foreground", className)}
      numberOfLines={1}
    >
      {text}
    </Text>
  );
}
