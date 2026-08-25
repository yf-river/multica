"use client";

import { useState, type ReactElement, type ReactNode } from "react";
import { CalendarClock, CalendarDays } from "lucide-react";
import type { UpdateIssueRequest } from "@multica/core/types";
import {
  dateOnlyToLocalDate,
  formatShortDateOnly,
  isPastDateOnly,
  toDateOnly,
} from "@multica/core/issues/date";
import { Button } from "@multica/ui/components/ui/button";
import { Calendar } from "@multica/ui/components/ui/calendar";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { useT } from "../../../i18n";

const TRIGGER_CLASS_NAME =
  "flex items-center gap-1.5 cursor-pointer rounded px-1 -mx-1 hover:bg-accent/30 transition-colors";

interface DatePickerControlProps {
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  trigger?: ReactNode;
  triggerRender?: ReactElement;
  open?: boolean;
  onOpenChange?: (value: boolean) => void;
  align?: "start" | "center" | "end";
  defaultOpen?: boolean;
}

interface IssueDatePickerProps extends DatePickerControlProps {
  value: string | null;
  field: "due_date" | "start_date";
  icon: ReactNode;
  triggerLabel: ReactNode;
  clearLabel: ReactNode;
  dateClassName?: string;
}

function IssueDatePicker({
  value,
  field,
  onUpdate,
  icon,
  triggerLabel,
  clearLabel,
  dateClassName,
  trigger,
  triggerRender,
  open: controlledOpen,
  onOpenChange,
  align = "start",
  defaultOpen = false,
}: IssueDatePickerProps) {
  const [internalOpen, setInternalOpen] = useState(defaultOpen);
  const open = controlledOpen ?? internalOpen;
  const setOpen = onOpenChange ?? setInternalOpen;
  const date = dateOnlyToLocalDate(value);

  function updateDate(nextDate: Date | undefined) {
    onUpdate({ [field]: nextDate ? toDateOnly(nextDate) : null });
    setOpen(false);
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        className={triggerRender ? undefined : TRIGGER_CLASS_NAME}
        render={triggerRender}
      >
        {trigger ?? (
          <>
            {icon}
            {date ? (
              <span className={dateClassName}>
                {formatShortDateOnly(value, "zh-CN")}
              </span>
            ) : (
              <span className="text-muted-foreground">{triggerLabel}</span>
            )}
          </>
        )}
      </PopoverTrigger>
      <PopoverContent className="w-auto p-0" align={align}>
        <Calendar mode="single" selected={date} onSelect={updateDate} />
        {date && (
          <div className="border-t px-3 py-2">
            <Button
              variant="ghost"
              size="xs"
              onClick={() => updateDate(undefined)}
              className="text-muted-foreground hover:text-foreground"
            >
              {clearLabel}
            </Button>
          </div>
        )}
      </PopoverContent>
    </Popover>
  );
}

export function StartDatePicker({
  startDate,
  ...props
}: DatePickerControlProps & { startDate: string | null }) {
  const { t } = useT("issues");
  return (
    <IssueDatePicker
      {...props}
      value={startDate}
      field="start_date"
      icon={<CalendarClock className="h-3.5 w-3.5 text-muted-foreground" />}
      triggerLabel={t(($) => $.pickers.start_date.trigger_label)}
      clearLabel={t(($) => $.pickers.start_date.clear_action)}
    />
  );
}

export function DueDatePicker({
  dueDate,
  ...props
}: DatePickerControlProps & { dueDate: string | null }) {
  const { t } = useT("issues");
  return (
    <IssueDatePicker
      {...props}
      value={dueDate}
      field="due_date"
      icon={<CalendarDays className="h-3.5 w-3.5 text-muted-foreground" />}
      triggerLabel={t(($) => $.pickers.due_date.trigger_label)}
      clearLabel={t(($) => $.pickers.due_date.clear_action)}
      dateClassName={isPastDateOnly(dueDate) ? "text-destructive" : undefined}
    />
  );
}
