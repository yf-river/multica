// 议题状态分类的排序与展示配置。
import type { IssueStatusCategory } from "../../types";

// 下面这三个都是按“分类”而不是“状态 key”建索引的。一个工作区可以定义任意数量的
// 自定义状态，但每一个都恰好属于下面 8 个分类之一——于是看板列、展示配置、分页拉取
// 都保持固定形状。要先用工作区目录（`useIssueStatuses`）把状态 KEY 解析成它的分类，
// 再来索引这些配置。（MUL-6243）

/** 状态分类在列/列表中的固定展示顺序。 */
export const STATUS_ORDER: IssueStatusCategory[] = [
  "backlog",
  "todo",
  "in_progress",
  "in_review",
  "pending_release",
  "done",
  "blocked",
  "cancelled",
];

/** 所有状态分类的完整集合（顺序同 STATUS_ORDER）。 */
export const ALL_STATUSES: IssueStatusCategory[] = [
  "backlog",
  "todo",
  "in_progress",
  "in_review",
  "pending_release",
  "done",
  "blocked",
  "cancelled",
];

/** 每个状态分类对应的展示配置：文案、图标/悬停/分隔线/列背景色。 */
export const STATUS_CONFIG: Record<
  IssueStatusCategory,
  {
    label: string;
    iconColor: string;
    hoverBg: string;
    dividerColor: string;
    columnBg: string;
  }
> = {
  backlog: { label: "Backlog", iconColor: "text-muted-foreground", hoverBg: "hover:bg-accent", dividerColor: "bg-muted-foreground/40", columnBg: "bg-muted/40" },
  todo: { label: "Todo", iconColor: "text-muted-foreground", hoverBg: "hover:bg-accent", dividerColor: "bg-muted-foreground/40", columnBg: "bg-muted/40" },
  in_progress: { label: "In Progress", iconColor: "text-warning", hoverBg: "hover:bg-warning/10", dividerColor: "bg-warning", columnBg: "bg-warning/5" },
  in_review: { label: "In Review", iconColor: "text-success", hoverBg: "hover:bg-success/10", dividerColor: "bg-success", columnBg: "bg-success/5" },
  pending_release: { label: "Pending Release", iconColor: "text-violet-600 dark:text-violet-400", hoverBg: "hover:bg-violet-500/10", dividerColor: "bg-violet-500", columnBg: "bg-violet-500/5" },
  done: { label: "Done", iconColor: "text-info", hoverBg: "hover:bg-info/10", dividerColor: "bg-info", columnBg: "bg-info/5" },
  blocked: { label: "Blocked", iconColor: "text-destructive", hoverBg: "hover:bg-destructive/10", dividerColor: "bg-destructive", columnBg: "bg-destructive/5" },
  cancelled: { label: "Cancelled", iconColor: "text-muted-foreground", hoverBg: "hover:bg-accent", dividerColor: "bg-muted-foreground/40", columnBg: "bg-muted/40" },
};
