/**
 * Mobile PriorityIcon — react-native-svg implementation.
 *
 * Geometry mirrors packages/views/issues/components/priority-icon.tsx
 * (16×16 viewBox, 4 ascending bars, "none" rendered as a center dash). Bar
 * counts mirror packages/core/issues/config/priority.ts PRIORITY_CONFIG.bars
 * — Behavioral parity rule: same priority → same number of filled bars
 * across clients.
 *
 * Differences from web:
 *   - No urgent pulse animation in v1 (would need reanimated; defer until
 *     animation polish iteration).
 */
import Svg, { Rect } from "react-native-svg";
import type { IssuePriority } from "@multica/core/types";

// 与 Web 四象限的条数和颜色一致，原生端独立渲染 SVG。
const BARS: Record<IssuePriority, number> = {
  important_urgent: 4, important_not_urgent: 3,
  not_important_urgent: 2, not_important_not_urgent: 1,
};
const COLOR: Record<IssuePriority, string> = {
  important_urgent: "#dc2626", important_not_urgent: "#eab308",
  not_important_urgent: "#eab308", not_important_not_urgent: "#71717a",
};

export function PriorityIcon({
  priority,
  size = 14,
}: {
  priority: IssuePriority;
  size?: number;
}) {
  const filled = BARS[priority];
  const color = COLOR[priority];

  return (
    <Svg width={size} height={size} viewBox="0 0 16 16">
      {[0, 1, 2, 3].map((i) => {
        const y = 12 - (i + 1) * 3;
        const h = (i + 1) * 3;
        return (
          <Rect
            key={i}
            x={1 + i * 4}
            y={y}
            width={3}
            height={h}
            rx={0.5}
            fill={color}
            opacity={i < filled ? 1 : 0.2}
          />
        );
      })}
    </Svg>
  );
}
