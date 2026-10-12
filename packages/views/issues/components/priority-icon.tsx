import type { IssuePriority } from "@multica/core/types";
import { PRIORITY_CONFIG } from "@multica/core/issues/config";
import { AlertTriangle, Circle, Target, Zap } from "lucide-react";

export function PriorityIcon({
  priority,
  className = "",
  inheritColor = false,
}: {
  priority: IssuePriority | string;
  className?: string;
  inheritColor?: boolean;
}) {
  const cfg = priority in PRIORITY_CONFIG ? PRIORITY_CONFIG[priority as IssuePriority] : null;

  if (!cfg) return <Circle className={`h-3.5 w-3.5 text-muted-foreground shrink-0 ${className}`} />;
  const Icon =
    priority === "important_urgent"
      ? AlertTriangle
      : priority === "important_not_urgent"
        ? Target
        : priority === "not_important_urgent"
          ? Zap
          : Circle;
  return <Icon className={`h-3.5 w-3.5 ${inheritColor ? "" : cfg.color} shrink-0 ${className}`} />;
}
