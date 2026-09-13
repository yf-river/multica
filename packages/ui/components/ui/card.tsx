import * as React from "react"

import { cn } from "@multica/ui/lib/utils"

// 本文件是卡片（Card）组件族：带边框、圆角、阴影的内容容器，通常由
// Header（标题/描述/操作）+ Content + Footer 拼装，顶层图片会自动贴合圆角。

/**
 * Card：卡片容器。可放 Header/Content/Footer 或任意内容。
 * 顶层若放图片会自动裁成圆角；存在 Footer 时底部内边距让给 Footer。
 * `size` 控制内边距与字号档位（default/sm）。
 */
function Card({
  className,
  size = "default",
  ...props
}: React.ComponentProps<"div"> & { size?: "default" | "sm" }) {
  return (
    <div
      data-slot="card"
      data-size={size}
      className={cn(
        "group/card flex flex-col gap-4 overflow-hidden rounded-xl border border-surface-border bg-surface py-4 text-body text-surface-foreground shadow-[var(--surface-shadow)] has-data-[slot=card-footer]:pb-0 has-[>img:first-child]:pt-0 data-[size=sm]:gap-3 data-[size=sm]:py-3 data-[size=sm]:has-data-[slot=card-footer]:pb-0 *:[img:first-child]:rounded-t-xl *:[img:last-child]:rounded-b-xl",
        className
      )}
      {...props}
    />
  )
}

/** CardHeader：卡片头部区，容纳标题、描述与右上角操作；可切换容器查询 */
function CardHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-header"
      className={cn(
        "group/card-header @container/card-header grid auto-rows-min items-start gap-1 rounded-t-xl px-4 group-data-[size=sm]/card:px-3 has-data-[slot=card-action]:grid-cols-[1fr_auto] has-data-[slot=card-description]:grid-rows-[auto_auto] [.border-b]:pb-4 group-data-[size=sm]/card:[.border-b]:pb-3",
        className
      )}
      {...props}
    />
  )
}

/** CardTitle：卡片标题 */
function CardTitle({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-title"
      className={cn(
        "font-heading text-title-sm leading-snug font-medium group-data-[size=sm]/card:text-body",
        className
      )}
      {...props}
    />
  )
}

/** CardDescription：卡片标题下的辅助说明文字 */
function CardDescription({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-description"
      className={cn("text-body text-muted-foreground", className)}
      {...props}
    />
  )
}

/** CardAction：钉在头部右上角的操作位（按钮/菜单等） */
function CardAction({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-action"
      className={cn(
        "col-start-2 row-span-2 row-start-1 self-start justify-self-end",
        className
      )}
      {...props}
    />
  )
}

/** CardContent：卡片主体内容区 */
function CardContent({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-content"
      className={cn("px-4 group-data-[size=sm]/card:px-3", className)}
      {...props}
    />
  )
}

/** CardFooter：卡片底部操作区，浅色底 + 顶部细分隔线 */
function CardFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-footer"
      className={cn(
        "flex items-center rounded-b-xl border-t border-surface-border bg-surface-hover/70 p-4 group-data-[size=sm]/card:p-3",
        className
      )}
      {...props}
    />
  )
}

export {
  Card,
  CardHeader,
  CardFooter,
  CardTitle,
  CardAction,
  CardDescription,
  CardContent,
}
