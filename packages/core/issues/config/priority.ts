// 议题安排四象限的排序与展示配置。
import type { IssuePriority } from "../../types";

/** 四象限从优先处理到最后处理的稳定排序。 */
export const PRIORITY_ORDER: IssuePriority[] = [
  "important_urgent",
  "important_not_urgent",
  "not_important_urgent",
  "not_important_not_urgent",
];

/** 所有面向用户的四象限列表展示顺序。 */
export const PRIORITY_DISPLAY_ORDER: IssuePriority[] = [
  "important_urgent",
  "important_not_urgent",
  "not_important_urgent",
  "not_important_not_urgent",
];

/** 每个四象限对应的展示配置：文案、条数、以及徽章的前景/背景色。 */
export const PRIORITY_CONFIG: Record<
  IssuePriority,
  { label: string; bars: number; color: string; badgeBg: string; badgeText: string }
> = {
  important_urgent: { label: "Important and urgent", bars: 4, color: "text-destructive", badgeBg: "bg-destructive/10", badgeText: "text-destructive" },
  important_not_urgent: { label: "Important, not urgent", bars: 3, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
  not_important_urgent: { label: "Not important, urgent", bars: 2, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
  not_important_not_urgent: { label: "Not important, not urgent", bars: 1, color: "text-muted-foreground", badgeBg: "bg-muted", badgeText: "text-muted-foreground" },
};
