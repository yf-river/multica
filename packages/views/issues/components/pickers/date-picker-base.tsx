"use client";

import { useState, type ReactElement, type ReactNode } from "react";
import type { UpdateIssueRequest } from "@multica/core/types";
import {
  dateOnlyToLocalDate,
  formatDateOnly,
  toDateOnly,
} from "@multica/core/issues/date";
import { Button } from "@multica/ui/components/ui/button";
import { Calendar } from "@multica/ui/components/ui/calendar";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";

const TRIGGER_CLASS_NAME =
  "flex items-center gap-1.5 cursor-pointer rounded px-1 -mx-1 hover:bg-accent/30 transition-colors";

interface IssueDatePickerBaseProps {
  value: string | null;
  field: "due_date" | "start_date";
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  icon: ReactNode;
  triggerLabel: ReactNode;
  clearLabel: ReactNode;
  dateClassName?: string;
  trigger?: ReactNode;
  triggerRender?: ReactElement;
  open?: boolean;
  onOpenChange?: (value: boolean) => void;
  align?: "start" | "center" | "end";
  defaultOpen?: boolean;
}

export function IssueDatePickerBase({
  value,
  field,
  onUpdate,
  icon,
  triggerLabel,
  clearLabel,
  dateClassName,
  trigger: customTrigger,
  triggerRender,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align = "start",
  defaultOpen = false,
}: IssueDatePickerBaseProps): ReactNode {
  const [internalOpen, setInternalOpen] = useState(defaultOpen);
  const open = controlledOpen ?? internalOpen;
  const setOpen = controlledOnOpenChange ?? setInternalOpen;
  const date = dateOnlyToLocalDate(value);

  function updateDate(nextDate: Date | undefined): void {
    const updates: Partial<UpdateIssueRequest> = {};
    updates[field] = nextDate ? toDateOnly(nextDate) : null;
    onUpdate(updates);
    setOpen(false);
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        className={triggerRender ? undefined : TRIGGER_CLASS_NAME}
        render={triggerRender}
      >
        {customTrigger ?? (
          <>
            {icon}
            {date ? (
              <span className={dateClassName}>
                {formatDateOnly(
                  value,
                  { month: "short", day: "numeric" },
                  "zh-CN",
                )}
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
