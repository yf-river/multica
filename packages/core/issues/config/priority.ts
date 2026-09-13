// 议题优先级的排序与展示配置。
import type { IssuePriority } from "../../types";

/**
 * 严重程度从高到低的顺序。这是**排序权重**——`sortIssues` 会把数组下标转成比较权重——
 * 所以 "none" 必须留在最后。绝不要为了改变菜单的阅读顺序去调整它；那种需求用
 * {@link PRIORITY_DISPLAY_ORDER}。
 */
export const PRIORITY_ORDER: IssuePriority[] = [
  "urgent",
  "high",
  "medium",
  "low",
  "none",
];

/**
 * 所有面向用户的优先级列表的展示顺序：空值在前，随后按严重程度降序。
 * 以“无优先级”开头，遵循应用里其它选择器的惯例——第一行永远是空值
 * （未分配 / 无项目 / 无阶段），这样无论点开哪个胶囊，眼睛都能在同一位置
 * 找到“清空该字段”。
 */
export const PRIORITY_DISPLAY_ORDER: IssuePriority[] = [
  "none",
  "urgent",
  "high",
  "medium",
  "low",
];

/** 每个优先级对应的展示配置：文案、条数、以及徽章的前景/背景色。 */
export const PRIORITY_CONFIG: Record<
  IssuePriority,
  { label: string; bars: number; color: string; badgeBg: string; badgeText: string }
> = {
  urgent: { label: "Urgent", bars: 4, color: "text-destructive", badgeBg: "bg-destructive/10", badgeText: "text-destructive" },
  high: { label: "High", bars: 3, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
  medium: { label: "Medium", bars: 2, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
  low: { label: "Low", bars: 1, color: "text-info", badgeBg: "bg-info/10", badgeText: "text-info" },
  none: { label: "No priority", bars: 0, color: "text-muted-foreground", badgeBg: "bg-muted", badgeText: "text-muted-foreground" },
};
