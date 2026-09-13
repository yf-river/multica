"use client";

// 本文件：DueDatePicker —— "截止日期"选择器。
// 薄封装通用 DateOnlyPicker：改值即调 onUpdate({ due_date })，并对已过期日期
// 高亮提示。支持受控/非受控两种用法（trigger 自带内容时按需惰性挂载浮层）。

import { CalendarDays } from "lucide-react";
import type { UpdateIssueRequest } from "@multica/core/types";
import { DateOnlyPicker } from "../../../common/date-only-picker";
import { useT } from "../../../i18n";

/**
 * 截止日期选择器：值变更 → `onUpdate({ due_date })`；`highlightOverdue`
 * 让过期日期以警示色呈现。
 */
export function DueDatePicker({
  dueDate,
  onUpdate,
  trigger,
  triggerRender,
  open,
  onOpenChange,
  align = "start",
  defaultOpen = false,
}: {
  dueDate: string | null;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement<Record<string, unknown>>;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  align?: "start" | "center" | "end";
  /** 首次挂载即展开浮层。供"渐进式展示"的侧栏用——新加的字段立刻进入编辑态。 */
  defaultOpen?: boolean;
}) {
  const { t } = useT("issues");
  return (
    <DateOnlyPicker
      value={dueDate}
      onChange={(v) => onUpdate({ due_date: v })}
      icon={<CalendarDays className="h-3.5 w-3.5 text-muted-foreground" />}
      placeholder={t(($) => $.pickers.due_date.trigger_label)}
      emptyLabel={t(($) => $.pickers.due_date.none)}
      highlightOverdue
      trigger={trigger}
      triggerRender={triggerRender}
      open={open}
      onOpenChange={onOpenChange}
      align={align}
      defaultOpen={defaultOpen}
    />
  );
}
