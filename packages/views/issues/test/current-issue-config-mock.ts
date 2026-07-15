import { vi } from "vitest";

vi.mock("@multica/core/issues/config", () => ({
  ALL_STATUSES: ["backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled"],
  BOARD_STATUSES: ["backlog", "todo", "in_progress", "in_review", "done", "blocked"],
  STATUS_ORDER: ["backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled"],
  STATUS_CONFIG: {
    backlog: { label: "待办池", iconColor: "text-muted-foreground", hoverBg: "hover:bg-accent" },
    todo: { label: "待处理", iconColor: "text-muted-foreground", hoverBg: "hover:bg-accent" },
    in_progress: { label: "进行中", iconColor: "text-warning", hoverBg: "hover:bg-warning/10" },
    in_review: { label: "评审中", iconColor: "text-success", hoverBg: "hover:bg-success/10" },
    done: { label: "已完成", iconColor: "text-info", hoverBg: "hover:bg-info/10" },
    blocked: { label: "已阻塞", iconColor: "text-destructive", hoverBg: "hover:bg-destructive/10" },
    cancelled: { label: "已取消", iconColor: "text-muted-foreground", hoverBg: "hover:bg-accent" },
  },
  PRIORITY_ORDER: ["urgent", "high", "medium", "low", "none"],
  PRIORITY_CONFIG: {
    urgent: { label: "Urgent", bars: 4, color: "text-destructive", badgeBg: "bg-destructive/10", badgeText: "text-destructive" },
    high: { label: "High", bars: 3, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
    medium: { label: "Medium", bars: 2, color: "text-warning", badgeBg: "bg-warning/10", badgeText: "text-warning" },
    low: { label: "Low", bars: 1, color: "text-info", badgeBg: "bg-info/10", badgeText: "text-info" },
    none: { label: "No priority", bars: 0, color: "text-muted-foreground", badgeBg: "bg-muted", badgeText: "text-muted-foreground" },
  },
}));
