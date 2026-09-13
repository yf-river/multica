// 通用日期选择器：各类"开始/截止日期"药丸背后的共享实现，用日历弹层选择日期，支持清除，日期以 "YYYY-MM-DD" 传输且无时区偏移。
"use client";

import { useState } from "react";
import {
  toDateOnly,
  dateOnlyToLocalDate,
  formatDateOnly,
  isPastDateOnly,
} from "@multica/core/issues/date";
import { Check } from "lucide-react";
import { Calendar } from "@multica/ui/components/ui/calendar";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import { DeferredPopup } from "./deferred-popup";
import { useLocale } from "../i18n";

/**
 * 日期药丸触发器的默认样式——与延迟版外观触发器共用，保证首次交互时的
 * 替换在像素级别完全一致。
 */
const DATE_TRIGGER_CLASS =
  "flex items-center gap-1.5 cursor-pointer rounded px-1 -mx-1 hover:bg-accent/30 transition-colors";

interface DateOnlyPickerProps {
  /** 选中的日历日（"YYYY-MM-DD"）或 null。 */
  value: string | null;
  /** 发出新的日历日，清除时发出 null。 */
  onChange: (value: string | null) => void;
  /** 触发器图标——实体按语义选 CalendarClock（开始）或 CalendarDays（截止）。 */
  icon: React.ReactNode;
  /** 未设置日期时显示的占位标签。 */
  placeholder: string;
  /** 弹层顶部空值行的标签（如"无截止日期"）。 */
  emptyLabel: string;
  /** 值为过去时间时用 `text-destructive` 高亮（用于截止日期）。 */
  highlightOverdue?: boolean;
  /** 完全自定义的触发器内容（替换图标 + 日期/占位）。 */
  trigger?: React.ReactNode;
  /** 自定义触发器元素（如药丸按钮）。 */
  triggerRender?: React.ReactElement<Record<string, unknown>>;
  /** 受控的打开状态——让 "⋯" 溢出菜单可以揭示并打开药丸。 */
  open?: boolean;
  /** 打开状态变化回调。 */
  onOpenChange?: (v: boolean) => void;
  /** 弹层对齐方式。 */
  align?: "start" | "center" | "end";
  /** 首次挂载时就打开弹层（渐进式披露侧栏）。 */
  defaultOpen?: boolean;
}

/**
 * 与实体无关的日历日选择器：所有开始/截止日期药丸（Issue、项目等）背后的
 * 共享行为。它负责 Popover + Calendar + 清除的接线，以及日历日的传输
 * （"YYYY-MM-DD"，无时区偏移，经 @multica/core/issues/date）；各实体包装它，
 * 只需提供字段名（通过 `onChange`）、图标和本地化文案。集中在一处能防止各
 * 实体的药丸在行为或显示格式上逐渐走偏。
 *
 * 非受控用法会先渲染一个延迟版外观触发器，在首次交互时才挂载 Popover +
 * Calendar 机制——默认触发器仅凭 props 就能算出，所以每个非受控药丸都会
 * 延迟。原因见 `DeferredPopup`。
 * @param props 日期选择器的属性集合。
 */
export function DateOnlyPicker(props: DateOnlyPickerProps) {
  const canDefer =
    props.open === undefined &&
    props.onOpenChange === undefined &&
    !props.defaultOpen;
  if (!canDefer) {
    return <DateOnlyPickerImpl {...props} />;
  }
  return (
    <DeferredPopup
      trigger={props.trigger ?? <DateTriggerContent {...props} />}
      triggerRender={props.triggerRender}
      triggerClassName={DATE_TRIGGER_CLASS}
    >
      {(open, onOpenChange) => (
        <DateOnlyPickerImpl {...props} open={open} onOpenChange={onOpenChange} />
      )}
    </DeferredPopup>
  );
}

/** 默认触发器内容，与 Impl 渲染的内容保持一致。 */
function DateTriggerContent({
  value,
  icon,
  placeholder,
  highlightOverdue = false,
}: Pick<DateOnlyPickerProps, "value" | "icon" | "placeholder" | "highlightOverdue">) {
  const locale = useLocale();
  const date = dateOnlyToLocalDate(value);
  const overdue = highlightOverdue && isPastDateOnly(value);
  return (
    <>
      {icon}
      {date ? (
        <span className={overdue ? "text-destructive" : ""}>
          {formatDateOnly(value, { month: "short", day: "numeric" }, locale)}
        </span>
      ) : (
        <span className="text-muted-foreground">{placeholder}</span>
      )}
    </>
  );
}

function DateOnlyPickerImpl({
  value,
  onChange,
  icon,
  placeholder,
  emptyLabel,
  highlightOverdue = false,
  trigger: customTrigger,
  triggerRender,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align = "start",
  defaultOpen = false,
}: DateOnlyPickerProps) {
  const [internalOpen, setInternalOpen] = useState(defaultOpen);
  const open = controlledOpen ?? internalOpen;
  const setOpen = controlledOnOpenChange ?? setInternalOpen;
  const date = dateOnlyToLocalDate(value);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        className={triggerRender ? undefined : DATE_TRIGGER_CLASS}
        render={triggerRender}
      >
        {customTrigger ?? (
          <DateTriggerContent
            value={value}
            icon={icon}
            placeholder={placeholder}
            highlightOverdue={highlightOverdue}
          />
        )}
      </PopoverTrigger>
      <PopoverContent className="w-auto p-0" align={align}>
        {/* 空值放最上面、在日历上方——和每个列表型选择器一致，所以无论打开
            哪个药丸，清除字段永远都是第一行。和底部按钮不同，它本身也是可选中
            值：字段为空时它带有对勾。 */}
        <button
          type="button"
          onClick={() => {
            onChange(null);
            setOpen(false);
          }}
          className="flex w-full items-center gap-3 border-b px-3 py-2 text-left text-body transition-colors hover:bg-accent"
        >
          <span className="flex min-w-0 flex-1 items-center gap-2 text-muted-foreground">
            {emptyLabel}
          </span>
          <Check className={`h-3.5 w-3.5 shrink-0 text-muted-foreground ${date ? "invisible" : ""}`} />
        </button>
        <Calendar
          mode="single"
          selected={date}
          onSelect={(d: Date | undefined) => {
            onChange(d ? toDateOnly(d) : null);
            setOpen(false);
          }}
        />
      </PopoverContent>
    </Popover>
  );
}
