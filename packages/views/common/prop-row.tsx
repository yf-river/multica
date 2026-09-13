// 详情页侧栏的两列属性行：左侧弱化标签 + 右侧弹性值，用 subgrid 让标签列自适应对齐。
import type { ReactNode } from "react";

/**
 * 详情页侧栏使用的两列属性行：左侧弱化标签，右侧弹性值。
 *
 * 使用 **subgrid**，因此父容器必须声明列轨道：
 *
 *   <div className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5">
 *     <PropRow label="…">…</PropRow>
 *     <PropRow label="…">…</PropRow>
 *   </div>
 *
 * `auto` 轨道按父网格中所有行里最宽的标签自适应，这样标签总能放得下、各行的值
 * 保持对齐，而无需挑一个魔法像素宽度。早期版本用固定的 `w-16` 标签；一旦某个
 * 标签（如 "Concurrency"）渲染宽于 64px 就会出问题——标签溢出到间隙里和值碰撞。
 *
 * `interactive`（默认 `true`）控制行是否有悬停高亮。大多数行包着一个
 * Picker/Popover 触发器，整行都可点击，所以高亮告诉用户"这是一个整体目标"。
 * 只读行（负责人 / 创建 / 更新）应传 `interactive={false}`，避免它们假装可点击。
 *
 * 使用场景：
 *   - Issue 详情侧栏（状态 / 优先级 / 负责人 / …）
 *   - 智能体详情检查面板（运行时 / 模型 / 可见性 / …）
 * @param label 左侧标签。
 * @param children 右侧值内容。
 * @param interactive 是否有悬停高亮（默认 true）。
 */
export function PropRow({
  label,
  children,
  interactive = true,
}: {
  label: ReactNode;
  children: ReactNode;
  interactive?: boolean;
}) {
  return (
    <div
      className={`-mx-2 col-span-2 grid min-h-8 grid-cols-subgrid items-center rounded-md px-2 ${
        interactive ? "transition-colors hover:bg-accent/50" : ""
      }`}
    >
      <span className="flex min-w-0 items-center gap-1.5 text-caption text-muted-foreground">
        {label}
      </span>
      <div className="flex min-w-0 items-center gap-1.5 truncate text-caption">
        {children}
      </div>
    </div>
  );
}
