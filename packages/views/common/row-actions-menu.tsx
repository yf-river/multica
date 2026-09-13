// 列表行操作菜单：把一行的动作收进一个常显触发器（触屏场景），以及行级 Enter/Space 激活判定。
"use client";

import { Fragment, type KeyboardEvent, type ReactNode } from "react";
import { MoreHorizontal } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";

/** 行操作菜单里的单个动作项。 */
export interface RowActionItem {
  /** 唯一标识。 */
  key: string;
  /** 动作图标。 */
  icon: ReactNode;
  /** 动作显示文案。 */
  label: string;
  /** 选中时执行的回调。 */
  onSelect: () => void;
  /** 是否为危险操作（红色样式）。 */
  danger?: boolean;
}

/**
 * 供自带控件的 `role="button"` 行做 Enter/Space 激活判定：在这些控件内部按下的
 * 键属于该控件，而不属于行本身。
 * @param e 键盘事件。
 * @param activate 行的激活回调。
 */
export function handleRowActivationKey(
  e: KeyboardEvent<HTMLElement>,
  activate: () => void,
) {
  if (e.target !== e.currentTarget) return;
  if (e.key !== "Enter" && e.key !== " ") return;
  e.preventDefault();
  activate();
}

/**
 * 一行的操作，收进一个始终可见的触发器后面。
 *
 * 这是列表行在指针界面上悬停才显示的操作控件的触屏对应物：粗指针既没有悬停
 * 也没有右键，否则永远够不到它们。它在主指针无法悬停的地方渲染、在能悬停的
 * 地方隐藏，让悬停控件接管——因此两者必须提供相同的动作。
 *
 * 切换条件是 `hover` 而非宽度断点：横屏手机超过了 `md` 却依然没有悬停能力，
 * 若用断点，它就会拿到自己永远无法触发的悬停控件。
 *
 * `groups` 按顺序渲染，组与组之间有分隔线。
 * @param label 触发器的可访问名称，如"聊天操作"。
 * @param groups 动作分组，按组渲染并加分隔线。
 */
export function RowActionsMenu({
  label,
  groups,
}: {
  /** 触发器的可访问名称，如"聊天操作"。 */
  label: string;
  /** 动作分组。 */
  groups: RowActionItem[][];
}) {
  const filled = groups.filter((group) => group.length > 0);
  if (filled.length === 0) return null;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <button
            type="button"
            aria-label={label}
            // 行负责点击选中；打开菜单时不能误触选中该行。
            onPointerDown={(e) => e.stopPropagation()}
            onClick={(e) => e.stopPropagation()}
            className="inline-flex size-8 shrink-0 items-center justify-center rounded text-muted-foreground outline-none transition-colors hover:bg-accent hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring data-popup-open:bg-accent data-popup-open:text-foreground [@media(hover:hover)]:hidden"
          >
            <MoreHorizontal className="size-4" />
          </button>
        }
      />
      <DropdownMenuContent align="end" className="w-auto">
        {filled.map((group, index) => (
          <Fragment key={group[0]!.key}>
            {index > 0 && <DropdownMenuSeparator />}
            {group.map((item) => (
              <DropdownMenuItem
                key={item.key}
                variant={item.danger ? "destructive" : "default"}
                onClick={item.onSelect}
              >
                {item.icon}
                {item.label}
              </DropdownMenuItem>
            ))}
          </Fragment>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
