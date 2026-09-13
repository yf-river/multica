"use client"
// 本文件是分栏拖拽（Resizable）组件族（基于 react-resizable-panels）：
// 让用户拖动分隔条来调整相邻面板的宽/高，用于可定制布局。

import * as ResizablePrimitive from "react-resizable-panels"

import { cn } from "@multica/ui/lib/utils"

/**
 * ResizablePanelGroup：分栏容器，横向（左右分）或纵向（上下分）排列面板。
 *
 * 这里关掉了库自带的拖动光标并把光标逻辑收归本组件控制，原因见下两处注释。
 */
function ResizablePanelGroup({
  className,
  ...props
}: ResizablePrimitive.GroupProps) {
  return (
    <ResizablePrimitive.Group
      data-slot="resizable-panel-group"
      // 库默认在拖动时会注入 `*, *:hover { cursor: … !important }`，并且某个
      // 面板一撞到边界就把光标收窄成单向箭头——于是拖动中光标会在两种图标间
      // 来回跳。这虽然「如实」，观感却像出 bug。改为由分隔条自身带
      // `cursor-col-resize`，光标全程不变。
      disableCursor
      className={cn(
        "flex h-full w-full aria-[orientation=vertical]:flex-col",
        // 拖动会捕获指针，指针随后会划过各面板——离开带光标的那个分隔条。
        // `:has` 在任一分隔条处于拖动态期间把同样光标套到整组上，等价于库的
        // 那条全局规则在 `disableCursor` 关掉之前做的事。不加这条的话，指针
        // 一离开 8px 宽的分隔条，光标就变回文本 I 形。
        "[&:has([data-separator=active])_*]:cursor-col-resize [&:has([data-separator=active])]:cursor-col-resize",
        "aria-[orientation=vertical]:[&:has([data-separator=active])_*]:cursor-row-resize aria-[orientation=vertical]:[&:has([data-separator=active])]:cursor-row-resize",
        className
      )}
      {...props}
    />
  )
}

/** ResizablePanel：可被拖拽调宽/高的面板，带 minSize/maxSize 等约束 */
function ResizablePanel({ ...props }: ResizablePrimitive.PanelProps) {
  return <ResizablePrimitive.Panel data-slot="resizable-panel" {...props} />
}

/**
 * ResizableHandle：面板间的分隔条。
 * @param props.withHandle 是否显示中间的小拖拽手柄装饰
 */
function ResizableHandle({
  withHandle,
  className,
  ...props
}: ResizablePrimitive.SeparatorProps & {
  withHandle?: boolean
}) {
  return (
    <ResizablePrimitive.Separator
      data-slot="resizable-handle"
      className={cn(
        // 全组件唯一的光标来源：分组容器退出了库自带光标逻辑
        //（见 ResizablePanelGroup 的说明）。悬停与整段拖动用同一种图标。
        "cursor-col-resize aria-[orientation=horizontal]:cursor-row-resize",
        "relative flex w-0 items-center justify-center before:absolute before:inset-y-0 before:left-1/2 before:w-px before:-translate-x-1/2 before:bg-transparent before:transition-colors hover:before:bg-foreground/15 data-[separator=active]:before:bg-foreground/15 after:absolute after:inset-y-0 after:left-1/2 after:w-2 after:-translate-x-1/2 focus-visible:outline-hidden aria-[orientation=horizontal]:h-0 aria-[orientation=horizontal]:w-full aria-[orientation=horizontal]:before:inset-x-0 aria-[orientation=horizontal]:before:inset-y-auto aria-[orientation=horizontal]:before:h-px aria-[orientation=horizontal]:before:w-full aria-[orientation=horizontal]:before:translate-x-0 aria-[orientation=horizontal]:after:left-0 aria-[orientation=horizontal]:after:h-2 aria-[orientation=horizontal]:after:w-full aria-[orientation=horizontal]:after:translate-x-0 aria-[orientation=horizontal]:after:-translate-y-1/2",
        className
      )}
      {...props}
    >
      {withHandle && (
        <div className="z-10 flex h-6 w-1 shrink-0 rounded-lg bg-border" />
      )}
    </ResizablePrimitive.Separator>
  )
}

export { ResizableHandle, ResizablePanel, ResizablePanelGroup }
