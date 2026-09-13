// 本文件：PriorityIcon —— 优先级图形（SVG）。
// 刻意分成两族：低/中/高共用同一"三档柱"骨架（亮 1/2/3 根、其余淡出），
// 让严重程度像"液位"一样一眼可读；紧急（urgent）跳出该刻度——一枚实心徽章
// 加感叹号，因为它是"打断"，不是"再高一档"；无优先级则是安静的一根短横线。

import type { IssuePriority } from "@multica/core/types";
import { PRIORITY_CONFIG } from "@multica/core/issues/config";

/**
 * 优先级图形，刻意分两族：
 * - 低/中/高共用三档柱骨架（亮 1/2/3 根、其余变淡），严重程度像液位一眼可读；
 * - 紧急（urgent）彻底跳出刻度——实心徽章 + 感叹号，因为它是"打断"而非"更高一档"；
 * - 无优先级保持安静的短横线。
 */
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

  // "无优先级"——一根简单横线
  if (!cfg || priority === "none") {
    return (
      <svg
        viewBox="0 0 16 16"
        className={`h-3.5 w-3.5 ${inheritColor ? "" : "text-muted-foreground"} shrink-0 ${className}`}
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
      >
        <line x1="3" y1="8" x2="13" y2="8" />
      </svg>
    );
  }

  if (priority === "urgent") {
    return (
      <svg
        viewBox="0 0 16 16"
        className={`h-3.5 w-3.5 ${inheritColor ? "" : cfg.color} shrink-0 ${className}`}
        fill="none"
      >
        <rect x="2" y="2" width="12" height="12" rx="3" fill="currentColor" />
        <line
          x1="8"
          y1="5"
          x2="8"
          y2="8.6"
          stroke="var(--background, #fff)"
          strokeWidth="1.7"
          strokeLinecap="round"
        />
        <circle cx="8" cy="11" r="0.95" fill="var(--background, #fff)" />
      </svg>
    );
  }

  // 低/中/高——固定三根柱的骨架；严重程度 = 亮起根数。
  const filled = Math.min(cfg.bars, 3);
  const BAR_HEIGHTS = [6, 9, 12];
  return (
    <svg
      viewBox="0 0 16 16"
      className={`h-3.5 w-3.5 ${inheritColor ? "" : cfg.color} shrink-0 ${className}`}
      fill="currentColor"
    >
      {BAR_HEIGHTS.map((height, i) => (
        <rect
          key={i}
          x={2 + i * 4.25}
          y={14 - height}
          width="3.5"
          height={height}
          rx="1"
          opacity={i < filled ? 1 : 0.35}
        />
      ))}
    </svg>
  );
}
