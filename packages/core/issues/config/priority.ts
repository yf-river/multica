import type { IssuePriority } from "../../types";

export const PRIORITY_ORDER: IssuePriority[] = [
  "urgent",
  "high",
  "medium",
  "low",
  "none",
];

export const PRIORITY_CONFIG: Record<
  IssuePriority,
  { bars: number; color: string; badgeBg: string; badgeText: string }
> = {
  urgent: { bars: 4, color: "text-destructive", badgeBg: "bg-destructive/10", badgeText: "text-destructive" },
  high: { bars: 3, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
  medium: { bars: 2, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
  low: { bars: 1, color: "text-info", badgeBg: "bg-info/10", badgeText: "text-info" },
  none: { bars: 0, color: "text-muted-foreground", badgeBg: "bg-muted", badgeText: "text-muted-foreground" },
};
