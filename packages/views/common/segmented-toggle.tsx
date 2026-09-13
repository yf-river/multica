// 分段切换控件：在互斥模式间做二选一，选中项浮到背景上、其余保持弱化，为每个二选一场景提供统一外观与交互。
"use client";

import type { ReactNode } from "react";
import { cn } from "@multica/ui/lib/utils";

/**
 * 用于在互斥模式间切换的药丸按钮组：选中项浮到背景上，其余保持弱化。为每个
 * "二选一"提供同一个控件，让模式切换无论出现在哪都长得一样、行为一致。
 * @param value 当前选中的值。
 * @param options 选项列表（值 + 展示内容）。
 * @param onChange 切换回调。
 * @param buttonClassName 覆盖紧凑默认尺寸（text-caption px-2 py-1）。
 */
export function SegmentedToggle<T extends string>({
  value,
  options,
  onChange,
  buttonClassName,
}: {
  value: T;
  options: ReadonlyArray<readonly [T, ReactNode]>;
  onChange: (value: T) => void;
  /** 覆盖紧凑默认尺寸（text-caption px-2 py-1）。 */
  buttonClassName?: string;
}) {
  return (
    <div className="grid auto-cols-fr grid-flow-col gap-1 rounded-md bg-muted p-1">
      {options.map(([key, label]) => (
        <button
          key={key}
          type="button"
          aria-pressed={value === key}
          onClick={() => {
            if (key !== value) onChange(key);
          }}
          className={cn(
            "rounded-sm font-medium transition-colors",
            buttonClassName ?? "px-2 py-1 text-caption",
            value === key
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {label}
        </button>
      ))}
    </div>
  );
}
