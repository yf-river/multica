"use client";

// 本文件：StartDatePicker —— "开始日期"选择器。
// 薄封装通用 DateOnlyPicker：改值即调 onUpdate({ start_date })。

import { CalendarClock } from "lucide-react";
import type { UpdateIssueRequest } from "@multica/core/types";
import { DateOnlyPicker } from "../../../common/date-only-picker";
import { useT } from "../../../i18n";

/**
 * 开始日期选择器：值变更 → `onUpdate({ start_date })`。
 */
export function StartDatePicker({
  startDate,
  onUpdate,
  trigger,
  triggerRender,
  open,
  onOpenChange,
  align = "start",
  defaultOpen = false,
}: {
  startDate: string | null;
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
      value={startDate}
      onChange={(v) => onUpdate({ start_date: v })}
      icon={<CalendarClock className="h-3.5 w-3.5 text-muted-foreground" />}
      placeholder={t(($) => $.pickers.start_date.trigger_label)}
      emptyLabel={t(($) => $.pickers.start_date.none)}
      trigger={trigger}
      triggerRender={triggerRender}
      open={open}
      onOpenChange={onOpenChange}
      align={align}
      defaultOpen={defaultOpen}
    />
  );
}
