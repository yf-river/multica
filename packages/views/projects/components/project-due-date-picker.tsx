"use client";

// 项目截止日期选择胶囊：共享 DateOnlyPicker 的薄封装，逾期时把过去日期标红。
import { CalendarDays } from "lucide-react";
import type { UpdateProjectRequest } from "@multica/core/types";
import { DateOnlyPicker } from "../../common/date-only-picker";
import { useT } from "../../i18n";

/**
 * 项目截止日期胶囊。共享 DateOnlyPicker 的薄封装（详见 ProjectStartDatePicker）；
 * `highlightOverdue` 会把已经过去的截止日期标红，和任务的截止日期胶囊一致。
 *
 * @param dueDate 当前截止日期（YYYY-MM-DD 或 null）
 * @param onUpdate 变更回调（携带局部更新对象）
 * @param triggerRender 自定义触发器（如创建弹窗里的药丸按钮）
 * @param align 弹层对齐方向
 * @param open 受控的打开状态——让 ⋯ 溢出菜单能「展开并打开」这个胶囊
 * @param onOpenChange 打开状态变化回调
 */
export function ProjectDueDatePicker({
  dueDate,
  onUpdate,
  triggerRender,
  align = "start",
  open,
  onOpenChange,
}: {
  dueDate: string | null;
  onUpdate: (updates: Partial<UpdateProjectRequest>) => void;
  /** 自定义触发器元素（例如创建弹窗里的胶囊按钮）。 */
  triggerRender?: React.ReactElement<Record<string, unknown>>;
  align?: "start" | "center" | "end";
  /** 受控打开状态——让 ⋯ 溢出菜单能展开并打开这个胶囊。 */
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
}) {
  const { t } = useT("projects");
  return (
    <DateOnlyPicker
      value={dueDate}
      onChange={(v) => onUpdate({ due_date: v })}
      icon={<CalendarDays className="h-3.5 w-3.5 text-muted-foreground" />}
      placeholder={t(($) => $.detail.prop_due_date)}
      emptyLabel={t(($) => $.detail.no_due_date)}
      highlightOverdue
      triggerRender={triggerRender}
      align={align}
      open={open}
      onOpenChange={onOpenChange}
    />
  );
}
