"use client";

import { CalendarClock } from "lucide-react";
import type { UpdateIssueRequest } from "@multica/core/types";
import { useT } from "../../../i18n";
import { IssueDatePickerBase } from "./date-picker-base";

export function StartDatePicker({
  startDate,
  onUpdate,
  trigger: customTrigger,
  triggerRender,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align = "start",
  defaultOpen = false,
}: {
  startDate: string | null;
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

  return (
    <IssueDatePickerBase
      value={startDate}
      field="start_date"
      onUpdate={onUpdate}
      icon={<CalendarClock className="h-3.5 w-3.5 text-muted-foreground" />}
      triggerLabel={t(($) => $.pickers.start_date.trigger_label)}
      clearLabel={t(($) => $.pickers.start_date.clear_action)}
      trigger={customTrigger}
      triggerRender={triggerRender}
      open={controlledOpen}
      onOpenChange={controlledOnOpenChange}
      align={align}
      defaultOpen={defaultOpen}
    />
  );
}
