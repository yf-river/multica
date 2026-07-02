"use client";

import { CalendarDays } from "lucide-react";
import type { UpdateIssueRequest } from "@multica/core/types";
import { isPastDateOnly } from "@multica/core/issues/date";
import { useT } from "../../../i18n";
import { IssueDatePickerBase } from "./date-picker-base";

export function DueDatePicker({
  dueDate,
  onUpdate,
  trigger: customTrigger,
  triggerRender,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align = "start",
  defaultOpen = false,
}: {
  dueDate: string | null;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  align?: "start" | "center" | "end";
  /** Open the popover on first mount. Used by progressive-disclosure
   *  sidebars so a newly-added field immediately enters edit state. */
  defaultOpen?: boolean;
}) {
  const { t } = useT("issues");
  const isOverdue = isPastDateOnly(dueDate);

  return (
    <IssueDatePickerBase
      value={dueDate}
      field="due_date"
      onUpdate={onUpdate}
      icon={<CalendarDays className="h-3.5 w-3.5 text-muted-foreground" />}
      triggerLabel={t(($) => $.pickers.due_date.trigger_label)}
      clearLabel={t(($) => $.pickers.due_date.clear_action)}
      dateClassName={isOverdue ? "text-destructive" : ""}
      trigger={customTrigger}
      triggerRender={triggerRender}
      open={controlledOpen}
      onOpenChange={controlledOnOpenChange}
      align={align}
      defaultOpen={defaultOpen}
    />
  );
}
