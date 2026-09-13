// 悬停勾选指示器：在 DropdownMenuCheckboxItem 行内悬停/聚焦时显示的复选标记，供各筛选下拉共用。
"use client";

import { Check } from "lucide-react";

// shadcn 官方模式（PR #6862）：在 DropdownMenuCheckboxItem 行内，条目悬停/聚焦
// 时出现的复选指示器。配合条目上的 FILTER_ITEM_CLASS 使用（隐藏内置指示器并
// 命名悬停组）。从 issues-header 抽出，让所有筛选下拉共用一份实现。

/** 配合 HoverCheck 使用的条目类名：隐藏内置指示器，并命名悬停组为 `fitem`。 */
export const FILTER_ITEM_CLASS =
  "group/fitem pr-1.5! [&>[data-slot=dropdown-menu-checkbox-item-indicator]]:hidden";

/**
 * 悬停勾选指示器。未选中时仅悬停/聚焦显示，选中时始终显示。
 * @param checked 是否选中。
 */
export function HoverCheck({ checked }: { checked: boolean }) {
  return (
    <div
      className="border-input data-[selected=true]:border-primary data-[selected=true]:bg-primary data-[selected=true]:text-primary-foreground pointer-events-none size-4 shrink-0 rounded-[4px] border transition-all select-none *:[svg]:opacity-0 data-[selected=true]:*:[svg]:opacity-100 opacity-0 group-hover/fitem:opacity-100 group-focus/fitem:opacity-100 data-[selected=true]:opacity-100"
      data-selected={checked}
    >
      <Check className="size-3.5 text-current" />
    </div>
  );
}
