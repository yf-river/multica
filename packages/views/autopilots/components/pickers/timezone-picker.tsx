// 时区选择器（带当前偏移展示）：选项列表由调用方传入（常用/浏览器/全量时区），
// 每项展示"城市名 + 相对 UTC 的当前偏移"，方便同名时区（如多处的 America/…）区分。
// 触发器以地球图标 + 城市名 + 偏移呈现。偏移按 10 分钟缓存，DST 边界后自动过期。

"use client";

import { useMemo, useState } from "react";
import { Check, ChevronDown, Globe } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import {
  PropertyPicker,
  PickerEmpty,
} from "../../../issues/components/pickers/property-picker";
import { useT } from "../../../i18n";

/**
 * TimezonePicker 组件参数：当前时区值、变更回调、候选时区列表、禁用态与触发器
 * 的可访问名称。
 */
export interface TimezonePickerProps {
  value: string;
  onChange: (tz: string) => void;
  options: string[];
  disabled?: boolean;
  className?: string;
  /** 触发器只显示选中的时区，因此它自己播报起来像"Shanghai"——一个有值没字段的
   *  控件。调用方给它命名这个字段。 */
  ariaLabel?: string;
}

// 每次查找都会构建一个 Intl.DateTimeFormat——全量 IANA 列表有数百个，而筛选框每敲
// 一个键都要来一遍。做缓存，但不永久：偏移会在 DST 边界移动，而桌面窗口一开数周，
// 三月缓存下来的条目不能到了四月还写着 "London GMT+0"。
const OFFSET_TTL_MS = 10 * 60_000;
const offsetCache = new Map<string, { offset: string; at: number }>();

// 取某时区当前偏移，带 TTL 缓存。
function offsetFor(tz: string): string {
  const now = Date.now();
  const cached = offsetCache.get(tz);
  if (cached !== undefined && now - cached.at < OFFSET_TTL_MS) return cached.offset;
  const offset = computeOffset(tz);
  offsetCache.set(tz, { offset, at: now });
  return offset;
}

// 用 Intl 的 shortOffset 算出形如 "GMT+8" 的当前偏移；非法时区返回空串。
function computeOffset(tz: string): string {
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: tz,
      timeZoneName: "shortOffset",
    }).formatToParts(new Date());
    return parts.find((p) => p.type === "timeZoneName")?.value ?? "";
  } catch {
    return "";
  }
}

// 城市标签：取 IANA 名的最后一段（把下划线换成空格），UTC 原样。
function cityLabel(tz: string): string {
  if (tz === "UTC") return "UTC";
  return tz.split("/").pop()?.replace(/_/g, " ") ?? tz;
}

/**
 * 带偏移显示的时区选择器。选项的搜索词同时覆盖 IANA 全名、城市名与当前偏移；
 * 触发器展示城市名 + 偏移，弹层内行同理。选中项在行首打勾。
 * @param value 当前选中的时区。
 * @param onChange 选择回调。
 * @param options 可选项列表。
 * @param disabled 是否禁用。
 * @param className 触发器附加样式。
 * @param ariaLabel 触发器可访问名称（用于命名该字段）。
 */
export function TimezonePicker({
  value,
  onChange,
  options,
  disabled,
  className,
  ariaLabel,
}: TimezonePickerProps) {
  const { t } = useT("autopilots");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");

  const selectedCity = cityLabel(value);
  const selectedOffset = offsetFor(value);

  const query = filter.trim().toLowerCase();
  const filteredOptions = useMemo(() => {
    if (!query) return options;
    return options.filter((tz) => {
      const haystack = `${tz} ${cityLabel(tz)} ${offsetFor(tz)}`.toLowerCase();
      return haystack.includes(query);
    });
  }, [options, query]);

  return (
    <PropertyPicker
      open={open}
      onOpenChange={(v) => {
        setOpen(v);
        if (!v) setFilter("");
      }}
      width="w-64"
      align="start"
      searchable
      searchPlaceholder={t(($) => $.timezone_picker.search_placeholder)}
      onSearchChange={setFilter}
      triggerRender={
        <button
          type="button"
          disabled={disabled}
          // 触发器的自身文本就是值，裸字段名会把值顶掉。可访问名携带整行可见内容——
          // 偏移正是区分两个同名时区的依据，也是它上屏的意义所在。
          aria-label={
            ariaLabel === undefined
              ? undefined
              : `${ariaLabel}: ${[selectedCity, selectedOffset].filter(Boolean).join(" ")}`
          }
          className={cn(
            "flex h-8 w-full items-center gap-1.5 rounded-lg border border-input bg-transparent px-2.5 text-body transition-colors outline-none",
            "hover:bg-accent/30",
            "focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50",
            "disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50",
            "dark:bg-input/30",
            className,
          )}
        />
      }
      trigger={
        <>
          <Globe className="size-3.5 shrink-0 text-muted-foreground" />
          <span className="flex-1 truncate text-left">{selectedCity}</span>
          {selectedOffset && (
            <span className="shrink-0 text-caption text-muted-foreground tabular-nums">
              {selectedOffset}
            </span>
          )}
          <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
        </>
      }
    >
      {/* 只在弹层开着时构建：每行的偏移查找都可能冷构建一个 Intl.DateTimeFormat，
          每次渲染全量 IANA 列表都要为这份可能根本没人看的列表在弹窗打开时（以及每次
          缓存过期时）付出那波爆发。 */}
      {!open ? null : filteredOptions.length === 0 ? (
        <PickerEmpty />
      ) : (
        filteredOptions.map((tz) => {
          const off = offsetFor(tz);
          const isSelected = tz === value;
          return (
            <button
              key={tz}
              type="button"
              data-picker-item
              onClick={() => {
                onChange(tz);
                setOpen(false);
              }}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-body transition-colors hover:bg-accent"
            >
              <span className="flex size-3.5 shrink-0 items-center justify-center">
                {isSelected && (
                  <Check className="size-3.5 text-foreground" />
                )}
              </span>
              <span className="flex-1 truncate text-left">{cityLabel(tz)}</span>
              {off && (
                <span className="shrink-0 text-caption text-muted-foreground tabular-nums">
                  {off}
                </span>
              )}
            </button>
          );
        })
      )}
    </PropertyPicker>
  );
}
